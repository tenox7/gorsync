package sender

import (
	"testing"

	"github.com/gokrazy/rsync/internal/rsyncwire"
)

// With --append the receiver sends only a sum head. Its block count is a
// claim from the peer and must not size an allocation: a head claiming 16M
// blocks used to cost 16M placeholder sums (640 MB), sorted afterwards.
func TestAppendHeadIgnoresClaimedCount(t *testing.T) {
	const allocLimit = 16 // MB, as in the fuzz tests
	var wire rsyncwire.Buffer
	wire.WriteInt32(1 << 24) // ChecksumCount
	wire.WriteInt32(700)     // BlockLength
	wire.WriteInt32(16)      // ChecksumLength
	wire.WriteInt32(0)       // RemainderLength
	st := fuzzTransfer(t, []byte(wire.String()))

	before := allocated()
	head, err := st.receiveSumsForPhase(true)
	after := allocated()
	if err != nil {
		t.Fatal(err)
	}
	if head.ChecksumCount != 1<<24 || head.BlockLength != 700 {
		t.Errorf("head = count %d, block %d; want the values sent", head.ChecksumCount, head.BlockLength)
	}
	if n := after - before; n > allocLimit*1024*1024 {
		t.Fatalf("allocated %d MB for a %d byte sum head", n/1024/1024, len(wire.String()))
	}
}
