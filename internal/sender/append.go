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
// With --append (mode 1), the sender does not verify the prefix; the receiver's
// existing bytes are trusted. --append-verify (mode 2) is parsed but currently
// behaves identically to mode 1; adding the prefix checksum is a future change.
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

	// Phase 1 (the redo pass) expects MD4 of the *whole* source file rather
	// than just the appended tail. Kick that off in parallel here so phase 1
	// doesn't have to re-read the source and so the sender never has to call
	// hashSearch — see sendFileAppendVerify.
	fullSumCh := st.appendSumPending(fileIndex)
	go func() {
		fh := md4.New()
		binary.Write(fh, binary.LittleEndian, st.Seed)
		f2, err := fl.source.Open(fl.path)
		if err != nil {
			fullSumCh <- nil
			return
		}
		defer f2.Close()
		var buf [chunkSize]byte
		if _, err := io.CopyBuffer(fh, f2, buf[:]); err != nil {
			fullSumCh <- nil
			return
		}
		fullSumCh <- fh.Sum(nil)
	}()

	if _, err := f.Seek(prefixSize, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
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
		if err := st.Conn.WriteInt32(int32(len(chunk))); err != nil {
			return err
		}
		if _, err := st.Conn.Writer.Write(chunk); err != nil {
			return err
		}
		h.Write(chunk)
		offset += int64(n)
	}
	if st.Opts.InfoGTE(rsyncopts.INFO_PROGRESS, 1) {
		st.Progress.Show(uint64(offset), true)
	}

	if err := st.Conn.WriteInt32(0); err != nil {
		return err
	}

	sum := h.Sum(nil)
	if _, err := st.Conn.Writer.Write(sum); err != nil {
		return err
	}
	return nil
}
