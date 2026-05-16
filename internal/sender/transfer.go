package sender

import (
	"io"
	"sync"

	"github.com/gokrazy/rsync/internal/log"
	"github.com/gokrazy/rsync/internal/progress"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncos"
	"github.com/gokrazy/rsync/internal/rsyncwire"
)

type Osenv struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// TransferOpts is a subset of Opts which is required for implementing a receiver.
type TransferOpts struct {
	Verbose bool
	DryRun  bool

	DeleteMode        bool
	PreserveGid       bool
	PreserveUid       bool
	PreserveLinks     bool
	PreservePerms     bool
	PreserveDevices   bool
	PreserveSpecials  bool
	PreserveTimes     bool
	PreserveHardlinks bool
}

type Transfer struct {
	// config
	// Opts *Opts
	Logger   log.Logger
	Opts     *rsyncopts.Options
	Env      *rsyncos.Env
	Progress progress.Printer
	Source   FileSource // for modules specifying a fs.FS

	// state
	Conn      *rsyncwire.Conn
	Seed      int32
	lastMatch int64

	// appendFullSums caches the MD4-of-full-source for files transferred in
	// --append phase 0, so phase 1's verify pass can echo it without
	// recomputing or doing delta. Keyed by file index. The channel signals
	// completion (and carries the MD4 bytes).
	appendFullSumsMu sync.Mutex
	appendFullSums   map[int32]chan []byte
}

// appendSumPending returns (and lazily creates) a channel that receives the
// MD4 of file fileIndex's full local source. Used by sendFileAppend to publish
// the value and sendFileAppendVerify to consume it.
func (st *Transfer) appendSumPending(fileIndex int32) chan []byte {
	st.appendFullSumsMu.Lock()
	defer st.appendFullSumsMu.Unlock()
	if st.appendFullSums == nil {
		st.appendFullSums = make(map[int32]chan []byte)
	}
	ch, ok := st.appendFullSums[fileIndex]
	if !ok {
		ch = make(chan []byte, 1)
		st.appendFullSums[fileIndex] = ch
	}
	return ch
}

//func (rt *Transfer) listOnly() bool { return rt.Dest == "" }
