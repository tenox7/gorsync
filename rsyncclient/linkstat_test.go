package rsyncclient_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncostest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// msgLog keeps what the client writes to its stderr: the server's messages.
type msgLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *msgLog) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}
func (m *msgLog) Close() error { return nil }
func (m *msgLog) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

// listOne asks gorsync's daemon, serving dir, for the file list of one
// requested path, and returns it with the messages the client got.
func listOne(t *testing.T, dir string, flags []string, requested string) (*rsyncclient.Result, string) {
	t.Helper()
	msgs := &msgLog{}
	client, err := rsyncclient.New(flags, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(msgs))
	if err != nil {
		t.Fatal(err)
	}
	mod := rsyncd.Module{Name: "tmp", Path: dir}
	srv, err := rsyncd.NewServer([]rsyncd.Module{mod}, rsyncd.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	stdinrd, stdinwr := io.Pipe()
	stdoutrd, stdoutwr := io.Pipe()
	conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<io.Pipe>")
	osenv := rsyncostest.New(t)
	pc := rsyncopts.NewContext(rsyncopts.NewOptions(osenv))
	if err := pc.ParseArguments(osenv, client.ServerCommandOptions(requested)); err != nil {
		t.Fatalf("parsing server args: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.InternalHandleConn(t.Context(), conn, &mod, pc)
	}()
	res, err := client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdoutrd, WriteCloser: stdinwr}, []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	return res, msgs.String()
}

const (
	sIFMT  = 0o170000
	sIFLNK = 0o120000
	sIFREG = 0o100000
	sIFDIR = 0o040000
)

// A requested path with no slash at its end names one entry. rsync looks at
// it with link_stat (lstat): with -l a link there is sent as the link. The
// daemon followed it, as fs.WalkDir stats its root, and sent the file it
// points to. Without -l, and for a folder's contents (a slash at the end),
// nothing changes.
func TestDaemonSendsANamedLinkAsTheLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	withL := []string{"-n", "-d", "-l"}
	for _, tc := range []struct {
		requested string
		flags     []string
		mode      int32
	}{
		{"link.txt", withL, sIFLNK},
		{"linkdir", withL, sIFLNK},
		{"a.txt", withL, sIFREG},
		{"sub", withL, sIFDIR},
		{"link.txt", []string{"-n", "-d"}, sIFREG}, // as before without -l
	} {
		res, msgs := listOne(t, dir, tc.flags, tc.requested)
		if len(res.FileList) != 1 || res.FileList[0].Name != tc.requested || res.FileList[0].Mode&sIFMT != tc.mode || res.IOErrors != 0 {
			t.Errorf("%v %s: file list %+v, io_error %d, messages %q; want %s itself, type %o",
				tc.flags, tc.requested, res.FileList, res.IOErrors, msgs, tc.requested, tc.mode)
		}
	}
	res, _ := listOne(t, dir, withL, "linkdir/")
	if got := names(res.FileList); !slices.Contains(got, "b.txt") {
		t.Errorf("linkdir/ with -l: file list %q, want the folder's contents, b.txt", got)
	}
}

// A requested path that is not there was only counted in io_error, which
// the daemon also sets for a path it cannot read: a client could not tell
// the two apart. rsync tells the client which, in a link_stat message.
func TestDaemonSaysANamedPathIsNotThere(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"nope.txt", "nodir/x.txt"} {
		res, msgs := listOne(t, dir, []string{"-n", "-d", "-l"}, p)
		if len(res.FileList) != 0 || res.IOErrors&1 == 0 || res.XferErrors == 0 ||
			!strings.Contains(msgs, "no such file or directory") || !strings.Contains(msgs, p) {
			t.Errorf("%s: file list %q, io_error %d, %d errors, messages %q; want a link_stat message that it is not there",
				p, names(res.FileList), res.IOErrors, res.XferErrors, msgs)
		}
	}
}
