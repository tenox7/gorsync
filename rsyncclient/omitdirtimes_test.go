package rsyncclient_test

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// pushStock sends src/ into dest with flags, stock rsync receiving.
func pushStock(t *testing.T, flags []string, src, dest string) {
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
	_, err = client.Run(t.Context(), &rsync.BothCloser{ReadCloser: stdout, WriteCloser: stdin}, []string{src + "/"})
	if werr := cmd.Wait(); err == nil {
		err = werr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// pushGorsyncd sends src/ into dest, served by gorsyncd as module mod.
func pushGorsyncd(t *testing.T, flags []string, src, dest string) {
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
	if _, err := client.RunDaemon(t.Context(), conn, "mod/", []string{src + "/"}); err != nil {
		t.Fatal(err)
	}
}

// -O (--omit-dir-times) leaves the times of the receiver's folders alone,
// and files still get theirs. gorsync rejected -O as an unknown option and
// did not send it to the server.
func TestOmitDirTimes(t *testing.T) {
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	fileTime := time.Date(2011, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, push := range map[string]func(*testing.T, []string, string, string){
		"stock rsync": pushStock,
		"gorsyncd":    pushGorsyncd,
	} {
		for _, omit := range []bool{false, true} {
			src, dest := t.TempDir(), t.TempDir()
			for root, rel := range map[string]string{src: "a/b/new.txt", dest: "a/b/old.txt"} {
				full := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(rel), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chtimes(filepath.Join(src, "a/b/new.txt"), fileTime, fileTime); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(filepath.Join(dest, "a"), old, old); err != nil {
				t.Fatal(err)
			}
			flags := []string{"-r", "-t"}
			if omit {
				flags = append(flags, "-O")
			}
			push(t, flags, src, dest)
			fi, err := os.Stat(filepath.Join(dest, "a"))
			if err != nil {
				t.Fatal(err)
			}
			if kept := fi.ModTime().Equal(old); kept != omit {
				t.Errorf("%s, %v: folder a has time %v; kept %v, want %v", name, flags, fi.ModTime(), kept, omit)
			}
			fi, err = os.Stat(filepath.Join(dest, "a/b/new.txt"))
			if err != nil || !fi.ModTime().Equal(fileTime) {
				t.Errorf("%s, %v: a/b/new.txt: %v; want the file with time %v", name, flags, err, fileTime)
			}
		}
	}
}
