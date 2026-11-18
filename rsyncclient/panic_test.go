package rsyncclient_test

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// panicConn panics in Read once armed, and more than limit bytes later.
type panicConn struct {
	net.Conn
	armed *atomic.Bool
	read  atomic.Int64
	limit int64
}

func (c *panicConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.armed.Load() && c.read.Add(int64(n)) > c.limit {
		panic("the connection broke")
	}
	return n, err
}

// receiveFrom runs a receiving client with args and opts against a
// gorsyncd that serves dir as module mod, into local, over the connection
// that wrap makes of the dialled one. The module is read only: gorsyncd
// writes nothing that a test's cleanup could race with, as it does not
// wait for a connection to end.
func receiveFrom(t *testing.T, dir string, wrap func(net.Conn) net.Conn, local string, args []string, opts ...rsyncclient.Option) error {
	t.Helper()
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dir}})
	client, err := rsyncclient.New(args, append(opts, rsyncclient.WithStderr(testlogger.New(t)), rsyncclient.DontRestrict())...)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("localhost", ts.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var c net.Conn = conn
	if wrap != nil {
		c = wrap(conn)
	}
	_, err = client.RunDaemon(t.Context(), c, "mod/", []string{local})
	return err
}

// sc R20: a panic in a run, in the library or in a callback it was given,
// came out of Run and ended the program: for sc, its TUI with every other
// transfer it ran, and the terminal left raw. Now Run returns it as an
// error, which says where it happened. errgroup's Wait passes on no panic
// of the goroutines a receiving run starts, so each one turns its own into
// an error, and closes the connection: the other does not wait for ever.
func TestRunReturnsAPanicAsAnError(t *testing.T) {
	isPanic := func(t *testing.T, err error, value string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "internal error: "+value) || !strings.Contains(err.Error(), "panic_test.go:") {
			t.Errorf("Run = %v, want the panic %q as an error, from panic_test.go", err, value)
		}
	}
	t.Run("send callback", func(t *testing.T) {
		// To a stock rsync, which has ended, and written all it will, when
		// the test does.
		src, dest := onlyTree(t), t.TempDir()
		client, err := rsyncclient.New([]string{"-r"}, rsyncclient.WithSender(), rsyncclient.WithStderr(testlogger.New(t)),
			rsyncclient.WithSendCallback(func(string) { panic("boom") }))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(rsynctest.TridgeOrGTFO(t, "stock rsync receives"), client.ServerCommandOptions(dest+"/")...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_, err = client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdout, WriteCloser: stdin}, []string{src + "/"})
		stdin.Close()
		stdout.Close()
		cmd.Wait()
		isPanic(t, err, "boom")
	})
	t.Run("file list callback", func(t *testing.T) {
		err := receiveFrom(t, onlyTree(t), nil, t.TempDir()+"/", []string{"-r"},
			rsyncclient.WithFileListCallback(func(rsync.FileInfo) { panic("boom") }))
		isPanic(t, err, "boom")
	})
	t.Run("receiver goroutine", func(t *testing.T) {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "big.bin"), bytes.Repeat([]byte("x"), 4<<20), 0o644); err != nil {
			t.Fatal(err)
		}
		// Armed by the file list, it panics in what reads the file's data:
		// the receiver, in a goroutine of its own.
		var armed atomic.Bool
		wrap := func(c net.Conn) net.Conn { return &panicConn{Conn: c, armed: &armed, limit: 1 << 20} }
		err := receiveFrom(t, src, wrap, t.TempDir()+"/", []string{"-r"},
			rsyncclient.WithFileListCallback(func(fi rsync.FileInfo) {
				if fi.Name == "big.bin" {
					armed.Store(true)
				}
			}))
		isPanic(t, err, "the connection broke")
	})
}
