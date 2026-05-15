package append_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
)

// TestBaselineSenderToSystemRsync establishes that gorsync sender → system rsync
// receiver works without --append. If this hangs, the issue is not specific to
// append support.
func TestBaselineSenderToSystemRsync(t *testing.T) {
	t.Parallel()

	rsyncBin := rsynctest.AnyRsync(t)

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	destDir := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	srcPath := filepath.Join(srcDir, "data.bin")
	full := writeRandom(t, srcPath, 16*1024)

	client, err := rsyncclient.New(
		[]string{"-t", "--inplace", "--partial", "-W"},
		rsyncclient.WithSender(),
		rsyncclient.WithStderr(testlogger.New(t)),
	)
	if err != nil {
		t.Fatal(err)
	}

	rsync := exec.Command(rsyncBin, client.ServerCommandOptions(destDir)...)
	t.Logf("rsync args: %q", rsync.Args)
	stdin, err := rsync.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := rsync.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderrBuf bytes.Buffer
	rsync.Stderr = &stderrBuf
	defer func() {
		if s := stderrBuf.String(); s != "" {
			t.Logf("rsync stderr:\n%s", s)
		}
	}()
	if err := rsync.Start(); err != nil {
		t.Fatal(err)
	}

	rw := &readWriter{Reader: stdout, Writer: stdin}
	if _, err := client.Run(context.Background(), rw, []string{srcPath}); err != nil {
		t.Fatalf("client.Run: %v", err)
	}
	_ = stdin.Close()
	if err := rsync.Wait(); err != nil {
		t.Fatalf("rsync.Wait: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("dest mismatch: got %d bytes, want %d", len(got), len(full))
	}
}
