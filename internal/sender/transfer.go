package sender

import (
	"io"

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

	// TellClient, when set, sends the client a message with tag, as an rsync
	// server tells it about a file it leaves out (rsync/sender.c
	// send_files); the client counts the message as an error. Servers set
	// it. Either way the run's io_error in Env records the file, which is
	// how a client sender reports it (rsyncclient.Result.IOErrors).
	TellClient func(tag uint8, msg string)

	// Only, when not nil, is all the sender sends: these paths, named as in
	// the file list, and the folders above them, as rsync's --files-from.
	// A listed path that is not there sets io_error.
	Only []string

	// OnSend, when set, is called with each file's name, as in the file
	// list, as the sender begins to send it: once per file, so not again
	// for a file the redo pass sends again. A client counts the files it
	// sends with it, instead of reading its --progress output, where a file
	// sent within a second has a progress line with no \r to mark it.
	OnSend func(name string)

	// state
	redo      bool // in the redo pass: OnSend told of its files already
	Conn      *rsyncwire.Conn
	Seed      int32
	lastMatch int64
}

// rsync's io_error bits (rsync.h IOERR_GENERAL, IOERR_VANISHED).
const (
	ioErrGeneral  = 1
	ioErrVanished = 2
)

// noteIOError adds bits to the run's io_error.
func (st *Transfer) noteIOError(bits int32) {
	if st.Env != nil {
		st.Env.IOErrors.Or(bits)
	}
}

//func (rt *Transfer) listOnly() bool { return rt.Dest == "" }
