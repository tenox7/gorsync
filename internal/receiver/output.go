package receiver

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

// outputMode controls how the receiver creates and finalizes destination
// files.
type outputMode struct {
	// Inplace causes writes to go directly to the destination file rather
	// than through a temporary file with an atomic rename. An interrupted
	// inplace transfer naturally leaves a partial destination, so Inplace
	// implies the keep-partial-on-failure behavior.
	Inplace bool

	// KeepPartial causes a partially written file to be kept on failure
	// (renamed into place) instead of removed. Ignored when Inplace is set.
	KeepPartial bool

	// PartialDir, when non-empty, is the directory (relative to the
	// destination root) where partial files are placed on failure. Only
	// consulted when KeepPartial is set and Inplace is not.
	PartialDir string

	// Fsync causes each written file to be fsynced before it is renamed
	// into place (--fsync).
	Fsync bool

	// Sparse leaves long runs of zeros as holes instead of writing them
	// (--sparse). Only honored when writing through a temporary file: in
	// Inplace mode a skipped run would keep the destination's old bytes where
	// the zeros belong, and real rsync punches holes there instead, which
	// this implementation does not do. Inplace writes the zeros.
	Sparse bool
}

const tempFilePerm os.FileMode = 0o600

// pendingFile is the receiver's write target for a single file. It supports
// two modes:
//
//   - Default: writes go to a temporary file in the destination directory
//     and a successful transfer ends with an atomic rename to the final
//     name. On failure the temporary file is removed (or, with KeepPartial,
//     renamed into place so the next transfer can resume from it).
//
//   - Inplace: writes go directly to the destination file. The receiver
//     relies on the sender to constrain its block-match algorithm to
//     forward references only; without that, an inplace transfer can
//     corrupt the destination during delta-sync.
type pendingFile struct {
	root      *os.Root
	finalPath string
	tmpPath   string
	f         *os.File
	mode      outputMode
	written   int64
	closed    bool
	done      bool
}

func newPendingFile(root *os.Root, fn string, mode outputMode) (*pendingFile, error) {
	if mode.Inplace {
		f, err := root.OpenFile(fn, os.O_WRONLY|os.O_CREATE, tempFilePerm)
		if err != nil {
			return nil, err
		}
		return &pendingFile{
			root:      root,
			finalPath: fn,
			tmpPath:   fn,
			f:         f,
			mode:      mode,
		}, nil
	}

	tmpPath, f, err := openTempInRoot(root, fn)
	if err != nil {
		return nil, err
	}
	return &pendingFile{
		root:      root,
		finalPath: fn,
		tmpPath:   tmpPath,
		f:         f,
		mode:      mode,
	}, nil
}

// openTempInRoot creates a uniquely named temporary file in the same
// directory as fn (relative to root), so an atomic rename to fn does not
// cross a mount point.
func openTempInRoot(root *os.Root, fn string) (string, *os.File, error) {
	dir, base := filepath.Split(fn)
	prefix := filepath.Join(dir, "."+base+".")
	for attempt := 0; attempt < 10000; attempt++ {
		name := prefix + strconv.FormatUint(rand.Uint64(), 10)
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, tempFilePerm)
		if err == nil {
			return name, f, nil
		}
		if !os.IsExist(err) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("could not allocate temp file for %s", fn)
}

func (p *pendingFile) Name() string { return p.finalPath }

func (p *pendingFile) Write(b []byte) (int, error) {
	if p.sparse() {
		return p.sparseWrite(b)
	}
	n, err := p.f.Write(b)
	p.written += int64(n)
	return n, err
}

// sparse reports whether runs of zeros become holes; see outputMode.Sparse.
func (p *pendingFile) sparse() bool { return p.mode.Sparse && !p.mode.Inplace }

// zeroBlock is the shortest run of zeros that is worth turning into a hole.
//
// rsync/fileio.c:SPARSE_WRITE_SIZE
var zeroBlock = make([]byte, 1024)

// sparseWrite skips over zeros instead of writing them, leaving a hole in the
// file. It positions every write itself (WriteAt at p.written), so the file
// offset that Write would use is never consulted.
//
// rsync/fileio.c:write_sparse
func (p *pendingFile) sparseWrite(buf []byte) (int, error) {
	rest := buf
	for len(rest) > 0 {
		data := bytes.TrimLeft(rest, "\x00")
		p.written += int64(len(rest) - len(data))
		rest = nil
		if idx := bytes.Index(data, zeroBlock); idx > -1 {
			data, rest = data[:idx], data[idx:]
		}
		if _, err := p.f.WriteAt(data, p.written); err != nil {
			return 0, err
		}
		p.written += int64(len(data))
	}
	return len(buf), nil
}

// SeekToAppendOffset positions the destination file at offset and accounts for
// the prefix bytes in the written counter so a subsequent Truncate(p.written)
// in CloseAtomicallyReplace doesn't lop off the existing prefix. Used by the
// --append receiver path.
//
// Only an in-place file holds that prefix. In a temporary file the skipped
// range reads back as zeros, and the rename would replace the destination's
// data with them, so refuse instead (rsyncopts makes --append imply --inplace).
func (p *pendingFile) SeekToAppendOffset(offset int64) error {
	if !p.mode.Inplace {
		return fmt.Errorf("append to %s needs --inplace: a temporary file would zero its first %d bytes", p.finalPath, offset)
	}
	if _, err := p.f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	p.written = offset
	return nil
}

// CloseAtomicallyReplace finalizes the transfer.
func (p *pendingFile) CloseAtomicallyReplace() error {
	if p.mode.Inplace {
		// The previous destination may have been longer; trim to the size
		// the sender actually delivered.
		if err := p.f.Truncate(p.written); err != nil {
			return err
		}
		if err := p.sync(); err != nil {
			return err
		}
		p.closed = true
		if err := p.f.Close(); err != nil {
			return err
		}
		p.done = true
		return nil
	}
	if p.sparse() {
		// The file might end in a hole: extend it to its full size.
		//
		// rsync/fileio.c:sparse_end
		if err := p.f.Truncate(p.written); err != nil {
			return err
		}
	}
	if err := p.sync(); err != nil {
		return err
	}
	p.closed = true
	if err := p.f.Close(); err != nil {
		return err
	}
	if err := p.root.Rename(p.tmpPath, p.finalPath); err != nil {
		return err
	}
	p.done = true
	return nil
}

// Cleanup is the error-path counterpart of CloseAtomicallyReplace and is
// safe to defer unconditionally — it is a no-op once the transfer has
// succeeded.
func (p *pendingFile) Cleanup() error {
	if p.done {
		return nil
	}
	if !p.closed {
		_ = p.f.Close()
		p.closed = true
	}
	if p.mode.Inplace {
		// Bytes have been written directly to the destination; an
		// inplace transfer cannot meaningfully roll back. The partial
		// file is left in place for the next sync to resume from.
		return nil
	}
	if !p.mode.KeepPartial {
		return p.root.Remove(p.tmpPath)
	}
	target := p.finalPath
	if p.mode.PartialDir != "" {
		if err := p.root.MkdirAll(p.mode.PartialDir, 0o755); err != nil {
			return err
		}
		target = filepath.Join(p.mode.PartialDir, filepath.Base(p.finalPath))
	}
	return p.root.Rename(p.tmpPath, target)
}

// sync flushes the file to stable storage, but only when --fsync was
// requested: an unconditional fsync per file costs a lot of throughput.
func (p *pendingFile) sync() error {
	if !p.mode.Fsync {
		return nil
	}
	return p.f.Sync()
}
