package receiver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/mmcloughlin/md4"
)

// rsync/receiver.c:recv_files
func (rt *Transfer) RecvFiles(fileList []*File) error {
	phase := 0
	for {
		idx, err := rt.Conn.ReadInt32()
		if err != nil {
			return err
		}
		if idx == -1 {
			if phase == 0 {
				phase++
				if rt.Opts.DebugGTE(rsyncopts.DEBUG_RECV, 1) {
					rt.Logger.Printf("recvFiles phase=%d", phase)
				}
				// TODO: send done message
				continue
			}
			break
		}
		if idx < 0 || int(idx) >= len(fileList) {
			return fmt.Errorf("protocol error: idx=%d out of bounds", idx)
		}

		if rt.Opts.DebugGTE(rsyncopts.DEBUG_RECV, 1) {
			rt.Logger.Printf("receiving file idx=%d: %+v", idx, fileList[idx])
		}
		if rt.Opts.Progress {
			fmt.Fprintln(rt.Env.Stdout, fileList[idx].Name)
		}
		if err := rt.recvFile1(fileList[idx]); err != nil {
			return err
		}
	}
	if rt.Opts.DebugGTE(rsyncopts.DEBUG_RECV, 1) {
		rt.Logger.Printf("recvFiles finished")
	}
	return nil
}

// partialName reports where a previously kept partial file for name lives, or
// "" when partials are not kept in a separate directory (in which case the
// partial was renamed to the destination name and openLocalFile finds it).
// The layout must match pendingFile.Cleanup in output.go.
func (rt *Transfer) partialName(name string) string {
	if rt.Opts.Inplace || !rt.Opts.KeepPartial || rt.Opts.PartialDir == "" {
		return ""
	}
	return filepath.Join(rt.Opts.PartialDir, filepath.Base(name))
}

func (rt *Transfer) recvFile1(f *File) error {
	if rt.Opts.DryRun {
		if !rt.Opts.Server {
			fmt.Fprintln(rt.Env.Stdout, f.Name)
		}
		return nil
	}

	st, err := rt.DestRoot.Lstat(f.Name)
	if err != nil || !st.Mode().IsRegular() {
		st = nil
	}
	perm := rt.destPerm(f, st)

	if partialName := rt.partialName(f.Name); partialName != "" {
		// Resume from the kept partial file, if any, regardless of
		// whether the localFile exists.
		partial, err := rt.DestRoot.Open(partialName)
		if err != nil && !os.IsNotExist(err) {
			rt.Logger.Printf("opening partial file failed, continuing: %v", err)
			// fallthrough to local file
		}
		if err == nil {
			// partial file exists; use it.
			defer partial.Close()
			if err := rt.receiveData(f, partial, perm); err != nil {
				return err
			}
			// receiveData called partial.Close()
			if err := rt.DestRoot.Remove(partialName); err != nil {
				return err
			}
			return nil
		}
		// fallthrough to local file
	}

	localFile, err := rt.openLocalFile(f)
	if err != nil && !os.IsNotExist(err) {
		rt.Logger.Printf("opening local file failed, continuing: %v", err)
	}
	defer localFile.Close()
	if err := rt.receiveData(f, localFile, perm); err != nil {
		return err
	}
	return nil
}

func (rt *Transfer) openLocalFile(f *File) (*os.File, error) {
	in, err := rt.DestRoot.Open(f.Name)
	if err != nil {
		return nil, err
	}

	st, err := in.Stat()
	if err != nil {
		return nil, err
	}

	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", filepath.Join(rt.Dest, f.Name))
	}

	if !st.Mode().IsRegular() {
		return nil, nil
	}

	return in, nil
}

// rsync/receiver.c:receive_data
func (rt *Transfer) receiveData(f *File, localFile *os.File, perm fs.FileMode) error {
	rt.Progress.Reset(uint64(f.Length))
	var sh rsync.SumHead
	if err := sh.ReadFrom(rt.Conn); err != nil {
		return err
	}

	if rt.Opts.DebugGTE(rsyncopts.DEBUG_DELTASUM, 1) {
		local := filepath.Join(rt.Dest, f.Name)
		rt.Logger.Printf("creating %s", local)
	}
	out, err := newPendingFile(rt.DestRoot, f.Name, outputMode{
		Inplace:     rt.Opts.Inplace,
		KeepPartial: rt.Opts.KeepPartial,
		PartialDir:  rt.Opts.PartialDir,
		Fsync:       rt.Opts.DoFsync,
	})
	if err != nil {
		return err
	}
	defer out.Cleanup()

	h := md4.New()
	binary.Write(h, binary.LittleEndian, rt.Seed)

	wr := io.MultiWriter(out, h)

	offset := 0
	if rt.Opts.AppendMode > 0 && sh.ChecksumCount > 0 {
		// Match rsync/receiver.c:receive_data — seek past the existing prefix
		// and append the incoming literals after it. MD4 is computed over the
		// new bytes only (plain --append: receiver trusts existing prefix).
		prefix := int64(sh.ChecksumCount) * int64(sh.BlockLength)
		if sh.RemainderLength != 0 {
			prefix -= int64(sh.BlockLength) - int64(sh.RemainderLength)
		}
		if err := out.SeekToAppendOffset(prefix); err != nil {
			return fmt.Errorf("append: seek to %d: %w", prefix, err)
		}
		offset = int(prefix)
	}
	for {
		token, data, err := rt.recvToken()
		if err != nil {
			return err
		}
		if token == 0 {
			break
		}
		if rt.Opts.Progress && !rt.Opts.Server {
			rt.Progress.MaybeShow(uint64(offset), false)
			if offset == 0 {
				defer func() {
					rt.Progress.MaybeShow(uint64(offset), true)
				}()
			}
		}
		if token > 0 {
			n, err := wr.Write(data)
			if err != nil {
				return err
			}
			offset += n
			continue
		}
		if localFile == nil {
			return fmt.Errorf("BUG: local file %s not open for copying chunk", out.Name())
		}
		token = -(token + 1)
		offset2 := int64(token) * int64(sh.BlockLength)
		dataLen := sh.BlockLength
		if token == sh.ChecksumCount-1 && sh.RemainderLength != 0 {
			dataLen = sh.RemainderLength
		}
		data = make([]byte, dataLen)
		if _, err := localFile.ReadAt(data, offset2); err != nil {
			return err
		}

		n, err := wr.Write(data)
		if err != nil {
			return err
		}
		offset += n
	}
	localSum := h.Sum(nil)
	remoteSum := make([]byte, len(localSum))
	if _, err := io.ReadFull(rt.Conn.Reader, remoteSum); err != nil {
		return err
	}
	if !bytes.Equal(localSum, remoteSum) {
		return fmt.Errorf("file corruption in %s", f.Name)
	}
	if rt.Opts.DebugGTE(rsyncopts.DEBUG_DELTASUM, 1) {
		rt.Logger.Printf("checksum %x matches!", localSum)
	}

	if localFile != nil {
		// Close the file earlier than the calling function’s deferred Close(),
		// so that we can rename files on Windows, which fails as long
		// as there are any open file handles.
		localFile.Close()
	}

	if err := out.CloseAtomicallyReplace(); err != nil {
		return err
	}

	if err := rt.setPerms(f, perm); err != nil {
		return err
	}

	return nil
}
