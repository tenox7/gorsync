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
