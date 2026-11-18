package sender

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync/atomic"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/panicerr"
	"github.com/gokrazy/rsync/internal/rsyncchecksum"
	"github.com/gokrazy/rsync/internal/rsynccommon"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncwire"
	"github.com/mmcloughlin/md4"
	"golang.org/x/sync/errgroup"
)

// rsync/sender.c:send_files()
func (st *Transfer) SendFiles(fileList *fileList) error {
	phase := 0
	for {
		// receive data about receiver’s copy of the file list contents (not
		// ordered)
		// see (*rsync.Receiver).Generator()
		fileIndex, err := st.Conn.ReadInt32()
		if err != nil {
			return err
		}
		if fileIndex == -1 {
			if phase == 0 {
				phase++
				st.redo = true
				// acknowledge phase change by sending -1
				if err := st.Conn.WriteInt32(-1); err != nil {
					return err
				}
				continue
			}
			break
		}

		if fileIndex < 0 || int(fileIndex) >= len(fileList.Files) {
			return fmt.Errorf("protocol error: idx=%d out of bounds", fileIndex)
		}

		if st.Opts.DryRun() {
			if err := st.Conn.WriteInt32(fileIndex); err != nil {
				return err
			}
			continue
		}

		fl := fileList.Files[fileIndex]
		st.Progress.Reset(uint64(fl.Length))

		// In phase 0 with --append, the receiver sends only the sum head; in
		// phase 1 (the redo pass) the receiver reverts to the normal protocol
		// and sends real per-block sums. receiveSums needs to know which.
		appendShortcut := phase == 0 && st.Opts.AppendMode() > 0
		head, err := st.receiveSumsForPhase(appendShortcut)
		if err != nil {
			return err
		}

		// The following quotes are citations from
		// https://www.samba.org/~tridge/phd_thesis.pdf, section 3.2.6 The
		// signature search algorithm (PDF page 64).

		// rsync/match.c:build_hash_table
		targets := make([]target, len(head.Sums))
		tagTable := make(map[uint16]int) // TODO: or int32 more specifically?
		{
			// “The first step in the algorithm is to sort the received
			// signatures by a 16 bit hash of the fast signature.”
			for idx, sum := range head.Sums {
				targets[idx] = target{
					index: int32(idx),
					tag:   rsyncchecksum.Tag(sum.Sum1),
				}
			}
			sort.Slice(targets, func(i, j int) bool {
				return targets[i].tag < targets[j].tag
			})

			// “A 16 bit index table is then formed which takes a 16 bit hash
			// value and gives an index into the sorted signature table which
			// points to the first entry in the table which has a matching
			// hash.”
			for idx := len(head.Sums) - 1; idx >= 0; idx-- {
				tagTable[targets[idx].tag] = idx
			}
		}

		st.lastMatch = 0
		switch {
		case appendShortcut && head.ChecksumCount > 0:
			// receiver has a partial prefix; stream only the trailing bytes
			err = st.sendFileAppend(head, fileIndex, fl)
		case len(head.Sums) == 0:
			// fast path: send the whole file
			err = st.sendFile(fileIndex, fl)
		case phase > 0 && st.Opts.AppendMode() > 0:
			// The redo pass of an --append transfer only holds files whose
			// checksum the receiver failed in phase 0: the prefix it kept
			// differs from ours. rsync turns append off for the redo
			// (rsync/sender.c:send_files) and resends; send the whole file
			// rather than trust any of the receiver's blocks.
			err = st.sendFile(fileIndex, fl)
		default:
			err = st.hashSearch(targets, tagTable, head, fileIndex, fl)
		}
		if err != nil {
			if _, ok := err.(*os.PathError); ok {
				// OpenFile() failed. Like rsync, leave the file out, count
				// it in the run's io_error and go on; a server tells the
				// client, which fails the run (rsync/sender.c send_files).
				// Only starting with protocol 30, an I/O error flag is
				// sent after the file transfer phase.
				if os.IsNotExist(err) {
					st.Logger.Printf("file has vanished: %s", fl.path)
					st.noteIOError(ioErrVanished)
					// A daemon below protocol 28 sends this as an error.
					st.tellClient(rsyncwire.MsgError, fmt.Sprintf("file has vanished: \"%s\"\n", fl.Wpath))
				} else {
					st.Logger.Printf("sendFiles: %v", err)
					st.noteIOError(ioErrGeneral)
					st.tellClient(rsyncwire.MsgErrorXfer, fmt.Sprintf("rsync: send_files failed to open \"%s\": %v\n", fl.Wpath, errors.Unwrap(err)))
				}
				continue
			} else {
				return err
			}
		}
	}

	// phase done
	if err := st.Conn.WriteInt32(-1); err != nil {
		return err
	}

	return nil
}

// rsync/sender.c:receive_sums()
func (st *Transfer) receiveSums() (rsync.SumHead, error) {
	return st.receiveSumsForPhase(false)
}

// receiveSumsForPhase reads the receiver's sum head and (unless appendShortcut
// is set) the per-block sums. In --append phase 0, rsync's generator emits
// only the sum head and skips the per-block sums (see rsync/generator.c —
// `if (append_mode > 0 && f_copy < 0) return 0;`). Phase 1 (the redo pass)
// reverts to full sums even when --append is in effect.
func (st *Transfer) receiveSumsForPhase(appendShortcut bool) (rsync.SumHead, error) {
	var head rsync.SumHead
	if err := head.ReadFrom(st.Conn); err != nil {
		return head, err
	}
	if appendShortcut {
		// Only the head arrives. sendFileAppend needs the prefix length it
		// encodes, not one entry per block the peer claims to hold.
		return head, nil
	}
	var offset int64
	// Grow head.Sums as checksums arrive instead of trusting ChecksumCount
	// up front: a peer has to actually send the data it claims.
	for i := int32(0); i < head.ChecksumCount; i++ {
		shortChecksum, err := st.Conn.ReadInt32()
		if err != nil {
			return head, err
		}
		sb := rsync.SumBuf{
			Index:  i,
			Offset: offset,
			Sum1:   uint32(shortChecksum),
		}
		if i == head.ChecksumCount-1 && head.RemainderLength != 0 {
			sb.Len = int64(head.RemainderLength)
		} else {
			sb.Len = int64(head.BlockLength)
		}
		offset += sb.Len
		n, err := io.ReadFull(st.Conn.Reader, sb.Sum2[:head.ChecksumLength])
		if err != nil {
			return head, err
		}
		_ = n
		// st.logger.Printf("chunk[%d] len=%d offset=%.0f sum1=%08x, sum2=%x",
		// 	i, sb.len, float64(sb.offset), sb.sum1, sb.sum2[:n])
		head.Sums = append(head.Sums, sb)
	}
	return head, nil
}

// begin tells of a file the sender opened to send: its path on stdout, with
// --progress, and its name to OnSend.
func (st *Transfer) begin(fl file) {
	if !st.Opts.Server() &&
		st.Opts.InfoGTE(rsyncopts.INFO_NAME, 1) &&
		st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
		fmt.Fprintln(st.Env.Stdout, fl.path)
	}
	if st.OnSend != nil && !st.redo {
		st.OnSend(fl.Wpath)
	}
}

func (st *Transfer) sendFile(fileIndex int32, fl file) error {
	f, err := fl.source.Open(fl.path)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	if err := st.Conn.WriteInt32(fileIndex); err != nil {
		return err
	}

	sh := rsynccommon.SumSizesSqroot(fi.Size())
	if err := sh.WriteTo(st.Conn); err != nil {
		return err
	}

	st.begin(fl)

	h := md4.New()
	binary.Write(h, binary.LittleEndian, st.Seed)

	// Calculate the md4 hash in a goroutine.
	//
	// This allows an rsync connection to benefit from more than 1 core!
	//
	// We calculate the hash by opening the same file again and reading
	// independently. This keeps the hot loop below focused on shoveling data
	// into the network socket as quickly as possible.
	//
	// A send that fails stops the hash at its next read. It would read the
	// rest of the file for nothing: each try of a send over a link that
	// keeps dying read the whole source once more, beside the next try.
	var stop atomic.Bool
	defer stop.Store(true)
	var eg errgroup.Group
	eg.Go(func() (err error) {
		defer panicerr.Recover(&err) // Wait passes on no panic
		f, err := fl.source.Open(fl.path)
		if err != nil {
			return err
		}
		defer f.Close()
		buf := readBufs.Get().(*[readBufSize]byte)
		defer readBufs.Put(buf)
		// stopReader also hides WriteTo: *os.File has one, so CopyBuffer
		// would ignore buf and allocate a 32 KiB buffer of its own for
		// every file.
		if _, err := io.CopyBuffer(h, stopReader{f, &stop}, buf[:]); err != nil {
			return err
		}
		return nil
	})

	var offset int64
	buf := readBufs.Get().(*[readBufSize]byte)
	defer readBufs.Put(buf)
	for {
		if st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
			st.Progress.MaybeShow(uint64(offset), false)
		}
		n, err := f.Read(buf[:])
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		// chunk size (“rawtok” variable in openrsync); split into wire tokens
		// no larger than rsync.ChunkSize, which the receiver caps at rsync's
		// CHUNK_SIZE.
		data := buf[:n]
		for len(data) > 0 {
			m := min(len(data), rsync.ChunkSize)
			if err := st.Conn.WriteInt32(int32(m)); err != nil {
				return err
			}
			if _, err := st.Conn.Writer.Write(data[:m]); err != nil {
				return err
			}
			data = data[m:]
			offset += int64(m)
		}
	}
	if st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
		st.Progress.Show(uint64(offset), true)
	}
	// transfer finished:
	if err := st.Conn.WriteInt32(0); err != nil {
		return err
	}

	// whole file long checksum (16 bytes)
	if err := eg.Wait(); err != nil {
		return err
	}
	sum := h.Sum(nil)
	// st.logger.Printf("sum: %x (len = %d)", sum, len(sum))
	if _, err := st.Conn.Writer.Write(sum); err != nil {
		return err
	}
	return nil
}

// errSendStopped ends the hash of a file whose send failed.
var errSendStopped = errors.New("the send of this file stopped")

// stopReader reads r until stop is set, then fails.
type stopReader struct {
	r    io.Reader
	stop *atomic.Bool
}

func (s stopReader) Read(p []byte) (int, error) {
	if s.stop.Load() {
		return 0, errSendStopped
	}
	return s.r.Read(p)
}

func (st *Transfer) tellClient(tag uint8, msg string) {
	if st.TellClient != nil {
		st.TellClient(tag, msg)
	}
}
