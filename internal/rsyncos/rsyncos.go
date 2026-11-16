package rsyncos

import (
	"context"
	"io"
	"net"
	"sync/atomic"

	"github.com/gokrazy/rsync/internal/log"
)

type Env struct {
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	Stderr io.WriteCloser

	DontRestrict bool

	// DialContext, when set, replaces the default TCP dialer used to reach an
	// rsync:// daemon. Lets callers meter or route the connection.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// XferErrors counts the MSG_ERROR_XFER and MSG_ERROR messages the peer
	// sent; rsync itself turns any of them into exit code 23.
	XferErrors atomic.Int32

	// IOErrors is the io_error flag the sender sends after the file list
	// (protocol < 30): bit 1 (IOERR_GENERAL) when it could not read
	// something and left it out of the list, bit 2 (IOERR_VANISHED) when a
	// file vanished while it listed. rsync skips deletes on bit 1. A sender
	// may set the flag without sending any error message (gorsync's own
	// sender does), so XferErrors alone can miss a short file list. When
	// this side sends, its sender adds the bits for what it leaves out.
	IOErrors atomic.Int32

	logger log.Logger
}

func (s *Env) initLogger() {
	if s.logger == nil {
		s.logger = log.New(s.Stderr)
	}
}

func (s *Env) Logger() log.Logger {
	s.initLogger()
	return s.logger
}

func (s *Env) Logf(format string, v ...any) {
	s.initLogger()
	s.logger.Printf(format, v...)
}

func (s *Env) Restrict() bool { return !s.DontRestrict }
