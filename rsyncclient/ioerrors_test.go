package rsyncclient_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncostest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// unreadableTree makes src/ok/a and src/locked/b, then takes away read access
// to src/locked: a sender lists locked/ but none of its children.
func unreadableTree(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	src := filepath.Join(t.TempDir(), "src")
	for _, p := range []string{"ok/a", "locked/b"} {
		full := filepath.Join(src, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(src, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	return src
}

func names(list []rsync.FileInfo) []string {
	var out []string
	for _, fi := range list {
		out = append(out, fi.Name)
	}
	return out
}

// A file list the sender could not build in full ends with the io_error
// flag set. gorsync's own sender sends no error message for it, so the flag
// is the only sign that the list is short.
func TestResultIOErrorsFromGorsyncSender(t *testing.T) {
	src := unreadableTree(t)
	stderr := testlogger.New(t)
	client, err := rsyncclient.New([]string{"-n", "-r"}, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(stderr))
	if err != nil {
		t.Fatal(err)
	}
	mod := rsyncd.Module{Name: "tmp", Path: src}
	srv, err := rsyncd.NewServer([]rsyncd.Module{mod}, rsyncd.WithStderr(stderr))
	if err != nil {
		t.Fatal(err)
	}
	stdinrd, stdinwr := io.Pipe()
	stdoutrd, stdoutwr := io.Pipe()
	conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<io.Pipe>")
	osenv := rsyncostest.New(t)
	pc := rsyncopts.NewContext(rsyncopts.NewOptions(osenv))
	if err := pc.ParseArguments(osenv, client.ServerCommandOptions("./")); err != nil {
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
	if res.IOErrors&1 == 0 {
		t.Errorf("IOErrors = %d, want bit 1 set; file list %q", res.IOErrors, names(res.FileList))
	}
	if got := names(res.FileList); !slices.Contains(got, "ok/a") || !slices.Contains(got, "locked") {
		t.Errorf("file list %q, want ok/a and locked", got)
	}
}

// Stock rsync sets the flag too, and also sends an error message.
func TestResultIOErrorsFromStockRsync(t *testing.T) {
	src := unreadableTree(t)
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("no rsync binary")
	}
	client, err := rsyncclient.New([]string{"-n", "-r"}, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("rsync", client.ServerCommandOptions(src+"/")...)
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
	res, err := client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdout, WriteCloser: stdin}, []string{t.TempDir()})
	cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.IOErrors&1 == 0 || res.XferErrors == 0 {
		t.Errorf("IOErrors = %d, XferErrors = %d; want both set", res.IOErrors, res.XferErrors)
	}
}

// A list built in full has no flag.
func TestResultIOErrorsZeroWhenComplete(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("no rsync binary")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, err := rsyncclient.New([]string{"-n", "-r"}, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("rsync", client.ServerCommandOptions(src+"/")...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	res, err := client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdout, WriteCloser: stdin}, []string{t.TempDir()})
	cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.IOErrors != 0 || res.XferErrors != 0 {
		t.Errorf("IOErrors = %d, XferErrors = %d for a readable tree", res.IOErrors, res.XferErrors)
	}
}
