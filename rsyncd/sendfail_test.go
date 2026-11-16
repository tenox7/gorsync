package rsyncd_test

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// A daemon that cannot open a file a client asks for tells the client,
// which counts it as an error, as stock rsync does (rsync/sender.c
// send_files). gorsyncd only logged it, so the client saw a download that
// left the file out as complete.
func TestDaemonTellsClientWhatItCannotSend(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	for _, rel := range []string{"ok.txt", "secret.txt"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(dir, "secret.txt")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(secret, 0o644) })

	stock := startStockDaemon(t, dir)
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dir}})
	for name, addr := range map[string]string{
		"stock rsync": stock,
		"gorsyncd":    net.JoinHostPort("localhost", ts.Port),
	} {
		client, err := rsyncclient.New([]string{"-r"}, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)), rsyncclient.DontRestrict())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		dst := t.TempDir()
		res, err := client.RunDaemon(t.Context(), conn, "mod/", []string{dst + "/"})
		conn.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.XferErrors == 0 {
			t.Errorf("%s: download with secret.txt unreadable: XferErrors = 0", name)
		}
		if _, err := os.Stat(filepath.Join(dst, "ok.txt")); err != nil {
			t.Errorf("%s: ok.txt did not arrive: %v", name, err)
		}
	}
}
