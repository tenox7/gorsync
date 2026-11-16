package receiver

import (
	"io/fs"
	"os"

	"github.com/gokrazy/rsync/internal/log"
	"github.com/gokrazy/rsync/internal/progress"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncos"
	"github.com/gokrazy/rsync/internal/rsyncwire"
)

// TransferOpts is a subset of Opts which is required for implementing a receiver.
type TransferOpts struct {
	Verbose  bool
	DryRun   bool
	Server   bool
	Progress bool

	DeleteMode        bool
	PreserveGid       bool
	PreserveUid       bool
	PreserveLinks     bool
	PreservePerms     bool
	PreserveDevices   bool
	PreserveSpecials  bool
	PreserveTimes     bool
	PreserveHardlinks bool
	IgnoreTimes       bool
	AlwaysChecksum    bool
	WholeFile         bool

	// OmitDirTimes leaves the times of folders alone (--omit-dir-times),
	// with PreserveTimes too.
	OmitDirTimes bool

	Inplace     bool
	KeepPartial bool
	PartialDir  string

	// AppendMode controls --append / --append-verify on the receiver. 0 = off,
	// 1 = --append (trust existing prefix), 2 = --append-verify (checksum
	// verify the prefix on the sender). Only AppendMode > 0 changes the
	// generator's request: it sends sums for the existing partial prefix so
	// the sender knows to skip those bytes.
	AppendMode int

	// DoFsync fsyncs every written file (--fsync).
	DoFsync bool

	// SparseFiles turns long runs of zeros into holes (--sparse). It only
	// applies to files written through a temporary file; see outputMode.Sparse.
	SparseFiles bool

	// OnFile, when set, is called for every file list entry as it is
	// received, in wire order, before the list is sorted.
	OnFile func(*File)

	// UnverifiedAppend accepts an appended tail without the file sum check:
	// the sender's sum covers the whole file, so a destination that holds a
	// placeholder rather than the real prefix can never match it. The remote
	// sum is still read to keep the stream in step.
	UnverifiedAppend bool

	InfoGTE  func(rsyncopts.InfoLevel, uint16) bool
	DebugGTE func(rsyncopts.DebugLevel, uint16) bool
}

type Transfer struct {
	// config
	Logger   log.Logger
	Opts     *TransferOpts
	Dest     string
	DestRoot *os.Root
	Env      *rsyncos.Env
	Progress progress.Printer

	// Excluded, if non-nil, reports whether a destination path (relative to
	// DestRoot, slash-separated) is protected from deletion by the
	// client-supplied filter list. Consulted only when DeleteMode is on.
	Excluded func(name string) bool

	// TellClient, when set, sends the client a message with tag, as an rsync
	// server tells it about a file it could not delete (rsync/delete.c
	// delete_item); the client counts the message as an error. Servers set
	// it; a client counts the error in Env.XferErrors instead.
	TellClient func(tag uint8, msg string)

	// state
	Conn         *rsyncwire.Conn
	Seed         int32
	IOErrors     int32
	Users        map[int32]mapping
	Groups       map[int32]mapping
	defaultPerms fs.FileMode
	retouchDirs  []retouchDir
}

type retouchDir struct {
	f    *File
	perm fs.FileMode
}

func (rt *Transfer) listOnly() bool { return rt.Dest == "" }
