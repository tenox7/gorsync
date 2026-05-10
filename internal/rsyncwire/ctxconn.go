package rsyncwire

import (
	"context"
	"io"
	"sync"
)

type CtxConn struct {
	Inner io.ReadWriter
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

func WrapCtx(ctx context.Context, conn io.ReadWriter) (io.ReadWriter, func()) {
	cc := &CtxConn{Inner: conn, Ctx: ctx}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	if closer, ok := conn.(io.Closer); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-done:
			}
		}()
	}
	return cc, stop
}
