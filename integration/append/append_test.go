package append_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gokrazy/rsync/internal/rsyncopts"
	"github.com/gokrazy/rsync/internal/rsyncostest"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

var _ = fmt.Sprintf
var _ = time.Second

type readWriter struct {
	io.Reader
	io.Writer
}

func writeRandom(t *testing.T, path string, size int) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return buf
}

// TestAppendToSystemRsync exercises gorsync sender → system rsync receiver with
// a partial destination. The expected result: rsync sees that the destination
// is shorter than the source, appends the missing tail, leaves the prefix
// untouched.
func TestAppendToSystemRsync(t *testing.T) {
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
	full := writeRandom(t, srcPath, 64*1024)

	const prefixSize = 16 * 1024
	destPath := filepath.Join(destDir, "data.bin")
	if err := os.WriteFile(destPath, full[:prefixSize], 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"-t",
		"--inplace",
		"--partial",
		"-W",
		"--append",
	}
	client, err := rsyncclient.New(args,
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
	rsync.Stderr = testlogger.New(t)
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

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("dest after append: %d bytes, want %d; prefix-equal=%v",
			len(got), len(full), bytes.Equal(got[:prefixSize], full[:prefixSize]))
	}
}

// TestAppendGorsyncToGorsync exercises both ends of the protocol in gorsync.
// Gorsync sender talks to gorsync server-mode receiver over io.Pipe. Verifies
// the receiver's --append generator + sender's append path both work.
func TestAppendGorsyncToGorsync(t *testing.T) {
	t.Parallel()

	stderr := testlogger.New(t)
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
	full := writeRandom(t, srcPath, 48*1024)

	const prefixSize = 12 * 1024
	destPath := filepath.Join(destDir, "data.bin")
	if err := os.WriteFile(destPath, full[:prefixSize], 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{"-t", "--inplace", "--partial", "-W", "--append"}
	client, err := rsyncclient.New(args,
		rsyncclient.WithSender(),
		rsyncclient.WithStderr(stderr),
	)
	if err != nil {
		t.Fatal(err)
	}

	rsync, err := rsyncd.NewServer(nil, rsyncd.WithStderr(stderr))
	if err != nil {
		t.Fatal(err)
	}

	stdinrd, stdinwr := io.Pipe()
	stdoutrd, stdoutwr := io.Pipe()
	conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<io.Pipe>")
	osenv := rsyncostest.New(t)
	pc := rsyncopts.NewContext(rsyncopts.NewOptions(osenv))
	if err := pc.ParseArguments(osenv, client.ServerCommandOptions(destDir)); err != nil {
		t.Fatalf("parsing server args: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := rsync.InternalHandleConn(t.Context(), conn, nil, pc); err != nil {
			t.Errorf("InternalHandleConn: %v", err)
		}
	}()

	rw := &readWriter{Reader: stdoutrd, Writer: stdinwr}
	if _, err := client.Run(t.Context(), rw, []string{srcPath}); err != nil {
		t.Fatalf("client.Run: %v", err)
	}
	wg.Wait()

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("dest after append: %d bytes, want %d", len(got), len(full))
	}
}
