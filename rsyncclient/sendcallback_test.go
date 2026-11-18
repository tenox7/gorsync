package rsyncclient_test

import (
	"io"
	"net"
	"os/exec"
	"slices"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// pushWith sends src/ to dest as a client sender with args and opts: to a
// stock rsync receiver, or to a gorsyncd module.
func pushWith(t *testing.T, daemon bool, src, dest string, args []string, opts ...rsyncclient.Option) {
	t.Helper()
	opts = append(opts, rsyncclient.WithSender(), rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
	if daemon {
		ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dest, Writable: true}})
		client, err := rsyncclient.New(args, append(opts, rsyncclient.DontRestrict())...)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", net.JoinHostPort("localhost", ts.Port))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := client.RunDaemon(t.Context(), conn, "mod/", []string{src + "/"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	client, err := rsyncclient.New(args, opts...)
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
	cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

// WithSendCallback names each file a sending client sends, once, as in the
// file list, and no folder: a caller counts the files of a send with it.
// The same send again names none, its files' sizes and times matching
// there; with --ignore-times it names them all again.
func TestSendCallbackNamesEachFileItSends(t *testing.T) {
	for name, daemon := range map[string]bool{"stock rsync": false, "gorsyncd": true} {
		t.Run(name, func(t *testing.T) {
			src, dest := onlyTree(t), t.TempDir()
			only := []string{"a.txt", "d/b.txt", "d/e/f.txt", "empty"}
			all := []string{"a.txt", "d/b.txt", "d/e/f.txt"}
			for i, c := range []struct {
				args []string
				want []string
			}{
				{[]string{"-r", "-t"}, all},
				{[]string{"-r", "-t"}, nil},
				{[]string{"-r", "-t", "-I"}, all},
			} {
				var names []string
				pushWith(t, daemon, src, dest, c.args, rsyncclient.WithSendOnly(only),
					rsyncclient.WithSendCallback(func(name string) { names = append(names, name) }))
				slices.Sort(names)
				if !slices.Equal(names, c.want) {
					t.Errorf("push %d (%q) named %q, want %q", i+1, c.args, names, c.want)
				}
			}
		})
	}
}
