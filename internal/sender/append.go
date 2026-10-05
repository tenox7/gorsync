package sender

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/mmcloughlin/md4"
)

// sendFileAppend handles --append (and --append-verify) by streaming only the
// trailing bytes from the local source. The receiver's sum head describes the
// existing prefix as ChecksumCount blocks (the last possibly of RemainderLength
// bytes); the prefix size is the sum of those. The receiver, paired with
// --inplace, appends the literals after its existing data.
//
// Per rsync/match.c:match_sums, the sender emits NO block-reference tokens in
// append mode — only literal tokens. The receiver places them at the end of
// its existing file. Block-ref tokens would cause the receiver to duplicate
// the prefix.
//
// The trailing file sum covers the existing prefix as well as the tail, as
// rsync does for --append-verify, which below protocol 30 is what --append
// means too; a receiver whose prefix differs fails the file.
func (st *Transfer) sendFileAppend(head rsync.SumHead, fileIndex int32, fl file) error {
	f, err := fl.source.Open(fl.path)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	prefixSize := int64(head.ChecksumCount) * int64(head.BlockLength)
	if head.RemainderLength != 0 {
		prefixSize -= int64(head.BlockLength) - int64(head.RemainderLength)
	}

	if prefixSize > fi.Size() {
		return fmt.Errorf("append: receiver prefix %d > sender size %d for %s", prefixSize, fi.Size(), fl.path)
	}

	if err := st.Conn.WriteInt32(fileIndex); err != nil {
		return err
	}
	if err := head.WriteTo(st.Conn); err != nil {
		return err
	}

	if !st.Opts.Server() &&
		st.Opts.InfoGTE(rsyncopts.INFO_NAME, 1) &&
		st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
		fmt.Fprintln(st.Env.Stdout, fl.path)
	}

	h := md4.New()
	binary.Write(h, binary.LittleEndian, st.Seed)

	// The trailing sum covers the whole file, prefix included
	// (rsync/match.c:match_sums with append_mode 2, which is what every
	// append is below protocol 30), so the receiver can check the bytes it
	// already holds. Reading the prefix through the hash positions f at the
	// tail; the sum doubles as the phase-1 redo sum, see sendFileAppendVerify.
	if _, err := io.CopyN(h, f, prefixSize); err != nil {
		return err
	}
	buf := make([]byte, readBufSize)
	offset := prefixSize
	for {
		if st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
			st.Progress.MaybeShow(uint64(offset), false)
		}
		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}
		chunk := buf[:n]
		h.Write(chunk)
		// Split into wire tokens no larger than chunkSize (rsync's CHUNK_SIZE);
		// longer literal tokens are rejected by the receiver.
		for len(chunk) > 0 {
			m := min(len(chunk), chunkSize)
			if err := st.Conn.WriteInt32(int32(m)); err != nil {
				return err
			}
			if _, err := st.Conn.Writer.Write(chunk[:m]); err != nil {
				return err
			}
			chunk = chunk[m:]
		}
		offset += int64(n)
	}
	if st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
		st.Progress.Show(uint64(offset), true)
	}

	if err := st.Conn.WriteInt32(0); err != nil {
		return err
	}

	sum := h.Sum(nil)
	st.appendSumPending(fileIndex) <- sum
	if _, err := st.Conn.Writer.Write(sum); err != nil {
		return err
	}
	return nil
}
