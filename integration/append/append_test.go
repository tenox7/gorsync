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

func (*readWriter) Close() error { return nil }

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

const appendPrefix = 12 * 1024

// appendFixture writes a 48 KiB random source file and a destination file
// that holds its first appendPrefix bytes.
func appendFixture(t *testing.T) (srcPath, destDir string, full []byte) {
	t.Helper()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	destDir = filepath.Join(tmp, "dest")
	for _, dir := range []string{srcDir, destDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srcPath = filepath.Join(srcDir, "data.bin")
	full = writeRandom(t, srcPath, 48*1024)
	if err := os.WriteFile(filepath.Join(destDir, "data.bin"), full[:appendPrefix], 0o644); err != nil {
		t.Fatal(err)
	}
	return srcPath, destDir, full
}

// runAgainstServer runs client against an in-process gorsync server that
// parses serverArgs, and waits for both ends. When one end fails, closing
// the pipes makes the other end fail too instead of hanging.
func runAgainstServer(t *testing.T, client *rsyncclient.Client, serverArgs, paths []string) {
	t.Helper()
	srv, err := rsyncd.NewServer(nil, rsyncd.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	stdinrd, stdinwr := io.Pipe()
	stdoutrd, stdoutwr := io.Pipe()
	conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<io.Pipe>")
	osenv := rsyncostest.New(t)
	pc := rsyncopts.NewContext(rsyncopts.NewOptions(osenv))
	if err := pc.ParseArguments(osenv, serverArgs); err != nil {
		t.Fatalf("parsing server args %q: %v", serverArgs, err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := srv.InternalHandleConn(t.Context(), conn, nil, pc)
		stdoutwr.CloseWithError(err)
		stdinrd.CloseWithError(err)
		if err != nil {
			t.Errorf("InternalHandleConn: %v", err)
		}
	}()

	rw := &readWriter{Reader: stdoutrd, Writer: stdinwr}
	_, err = client.Run(t.Context(), rw, paths)
	if err != nil {
		stdinwr.CloseWithError(err)
		stdoutrd.CloseWithError(err)
	}
	wg.Wait()
	if err != nil {
		t.Fatalf("client.Run: %v", err)
	}
}

// checkAppended fails unless path holds full, and says so when the existing
// prefix was overwritten.
func checkAppended(t *testing.T, path string, full []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) >= appendPrefix && !bytes.Equal(got[:appendPrefix], full[:appendPrefix]) {
		t.Fatalf("the existing %d-byte prefix was overwritten (all zeros: %v)",
			appendPrefix, bytes.Count(got[:appendPrefix], []byte{0}) == appendPrefix)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("dest after append: %d bytes, want %d", len(got), len(full))
	}
}

// TestAppendStockClientArgs gives a gorsync server the arguments a stock rsync
// client sends for --append and --append-verify: no --inplace, because rsync's
// options.c makes --append imply it on both ends. Appending to a temporary
// file instead replaces the existing prefix with zeros, and the transfer still
// succeeds: the file checksum covers the prefix as read from the old file.
func TestAppendStockClientArgs(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--append", "--append-verify"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()

			srcPath, destDir, full := appendFixture(t)
			client, err := rsyncclient.New([]string{"-t", flag},
				rsyncclient.WithSender(),
				rsyncclient.WithStderr(testlogger.New(t)),
			)
			if err != nil {
				t.Fatal(err)
			}
			// gorsync's client also forwards --inplace; a stock client does not.
			var serverArgs []string
			for _, arg := range client.ServerCommandOptions(destDir) {
				if arg != "--inplace" {
					serverArgs = append(serverArgs, arg)
				}
			}
			runAgainstServer(t, client, serverArgs, []string{srcPath})
			checkAppended(t, filepath.Join(destDir, "data.bin"), full)
		})
	}
}

// TestAppendDownloadWithoutInplace is the same rule on the client side: a
// gorsync client receiving with --append alone must write into the existing
// file too.
func TestAppendDownloadWithoutInplace(t *testing.T) {
	t.Parallel()

	srcPath, destDir, full := appendFixture(t)
	client, err := rsyncclient.New([]string{"-t", "--append"},
		rsyncclient.WithStderr(testlogger.New(t)),
	)
	if err != nil {
		t.Fatal(err)
	}
	runAgainstServer(t, client, client.ServerCommandOptions(srcPath), []string{destDir})
	checkAppended(t, filepath.Join(destDir, "data.bin"), full)
}

// TestAppendSkipsNotShorter covers rsync/generator.c: --append leaves a
// destination alone when it is not shorter than the source. The receiver
// used to send block sums that no --append sender reads, so the transfer
// stopped with a protocol error at the first such file.
func TestAppendSkipsNotShorter(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	destDir := filepath.Join(tmp, "dest")
	for _, dir := range []string{srcDir, destDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const size = 32 * 1024
	writeRandom(t, filepath.Join(srcDir, "same.bin"), size)
	writeRandom(t, filepath.Join(srcDir, "longer.bin"), size)
	growing := writeRandom(t, filepath.Join(srcDir, "growing.bin"), size)
	want := map[string][]byte{
		"same.bin":    writeRandom(t, filepath.Join(destDir, "same.bin"), size),
		"longer.bin":  writeRandom(t, filepath.Join(destDir, "longer.bin"), size+1024),
		"growing.bin": growing,
	}
	if err := os.WriteFile(filepath.Join(destDir, "growing.bin"), growing[:appendPrefix], 0o644); err != nil {
		t.Fatal(err)
	}
	// An older mtime keeps the quick check (same size and mtime) from
	// skipping same.bin before the --append rule is reached.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for name := range want {
		if err := os.Chtimes(filepath.Join(destDir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}

	client, err := rsyncclient.New([]string{"-rt", "--append"},
		rsyncclient.WithSender(),
		rsyncclient.WithStderr(testlogger.New(t)),
	)
	if err != nil {
		t.Fatal(err)
	}
	runAgainstServer(t, client, client.ServerCommandOptions(destDir), []string{srcDir + "/"})

	for name, want := range want {
		got, err := os.ReadFile(filepath.Join(destDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, want the expected %d", name, len(got), len(want))
		}
	}
}
