package rsyncclient_test

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsynccmd"
	"github.com/gokrazy/rsync/rsyncd"
)

// sendTree makes src/ok.txt and src/<bad>, then takes read access away
// from src/<bad>: a file the sender can list but not open, or a folder it
// cannot list.
func sendTree(t *testing.T, bad string, dir bool) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	src := t.TempDir()
	files := []string{"ok.txt", bad}
	if dir {
		files[1] = bad + "/inside.txt"
	}
	for _, rel := range files {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(src, bad)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o755) })
	return src
}

func pushStockResult(t *testing.T, flags []string, src, dest string) *rsyncclient.Result {
	t.Helper()
	client, err := rsyncclient.New(flags, rsyncclient.WithSender(), rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
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
	res, err := client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdout, WriteCloser: stdin}, []string{src + "/"})
	cmd.Wait() // stock rsync exits 23: the file list it got says so
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func pushGorsyncdResult(t *testing.T, flags []string, src, dest string) *rsyncclient.Result {
	t.Helper()
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dest, Writable: true}})
	client, err := rsyncclient.New(flags, rsyncclient.WithSender(), rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)), rsyncclient.DontRestrict())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("localhost", ts.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	res, err := client.RunDaemon(t.Context(), conn, "mod/", []string{src + "/"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A client that sends counts what it leaves out in Result.IOErrors, as rsync
// counts it in io_error and exits 23: a file it can list but not open, and
// a folder it cannot list. gorsync only logged them, so a push that left
// files behind looked complete.
func TestSenderCountsWhatItLeavesOut(t *testing.T) {
	for _, tc := range []struct {
		bad string
		dir bool
	}{
		{"secret.txt", false},
		{"locked", true},
	} {
		for name, push := range map[string]func(*testing.T, []string, string, string) *rsyncclient.Result{
			"stock rsync": pushStockResult,
			"gorsyncd":    pushGorsyncdResult,
		} {
			src, dest := sendTree(t, tc.bad, tc.dir), t.TempDir()
			res := push(t, []string{"-r", "-t"}, src, dest)
			if res.IOErrors&1 == 0 {
				t.Errorf("push to %s with %s unreadable: IOErrors = %d, want bit 1", name, tc.bad, res.IOErrors)
			}
			if _, err := os.Stat(filepath.Join(dest, "ok.txt")); err != nil {
				t.Errorf("push to %s with %s unreadable: ok.txt did not arrive: %v", name, tc.bad, err)
			}
		}
	}

	// rsynccmd reports it too.
	src := sendTree(t, "secret.txt", false)
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: t.TempDir(), Writable: true}})
	cmd := rsynccmd.Command("rsync", "-r", "-t", src+"/", fmt.Sprintf("rsync://localhost:%s/mod/", ts.Port))
	cmd.Stdout, cmd.Stderr = testlogger.New(t), testlogger.New(t)
	cmd.DontRestrict = true
	res, err := cmd.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.IOErrors&1 == 0 {
		t.Errorf("rsynccmd push with secret.txt unreadable: IOErrors = %d, want bit 1", res.IOErrors)
	}
}
