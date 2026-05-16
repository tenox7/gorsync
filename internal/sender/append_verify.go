package sender

import (
	"fmt"

	"github.com/gokrazy/rsync"
)

// sendFileAppendVerify handles the phase-1 redo pass for files that were
// transferred with --append in phase 0. Real rsync's generator in phase 1
// sends real per-block sums (rsync/match.c falls into hash_search), but the
// sender side does not need to compute or verify any local block checksums
// because the file is already complete on the receiver after phase 0. We
// emit a block-reference token for every block in the receiver's sum head
// — the receiver assembles its output from its own existing data — and then
// the MD4 of the full local source that sendFileAppend computed in parallel.
//
// This sidesteps the only remaining delta path in --append flows.
func (st *Transfer) sendFileAppendVerify(head rsync.SumHead, fileIndex int32, fl file) error {
	if err := st.Conn.WriteInt32(fileIndex); err != nil {
		return err
	}
	if err := head.WriteTo(st.Conn); err != nil {
		return err
	}

	for i := int32(0); i < head.ChecksumCount; i++ {
		if err := st.Conn.WriteInt32(-(i + 1)); err != nil {
			return err
		}
	}

	if err := st.Conn.WriteInt32(0); err != nil {
		return err
	}

	sum := <-st.appendSumPending(fileIndex)
	if sum == nil {
		return fmt.Errorf("append verify: full-file MD4 unavailable for %s", fl.path)
	}
	if _, err := st.Conn.Writer.Write(sum); err != nil {
		return err
	}
	return nil
}
