package rsyncwire

import (
	"context"
	"io"
	"sync"
)

type CtxConn struct {
	Inner io.ReadWriteCloser
	Ctx   context.Context
}

func (c *CtxConn) Read(p []byte) (int, error) {
	if err := c.Ctx.Err(); err != nil {
		return 0, err
	}
	return c.Inner.Read(p)
}

func (c *CtxConn) Write(p []byte) (int, error) {
	if err := c.Ctx.Err(); err != nil {
		return 0, err
	}
	return c.Inner.Write(p)
}

func (c *CtxConn) Close() error { return c.Inner.Close() }

func WrapCtx(ctx context.Context, conn io.ReadWriteCloser) (io.ReadWriteCloser, func()) {
	cc := &CtxConn{Inner: conn, Ctx: ctx}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return cc, stop
}
