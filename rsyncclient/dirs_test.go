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

// listGorsync runs a dry run with flags against gorsync's own sender,
// serving src as a module, for the path req in it.
func listGorsync(t *testing.T, src string, flags []string, req string) *rsyncclient.Result {
	t.Helper()
	stderr := testlogger.New(t)
	client, err := rsyncclient.New(flags, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(stderr))
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
	if err := pc.ParseArguments(osenv, client.ServerCommandOptions(req)); err != nil {
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
	return res
}

// listStock runs the same dry run against stock rsync as the sender, in src.
func listStock(t *testing.T, src string, flags []string, req string) *rsyncclient.Result {
	t.Helper()
	client, err := rsyncclient.New(flags, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("rsync", client.ServerCommandOptions(req)...)
	cmd.Dir = src
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
	return res
}

func sortedNames(res *rsyncclient.Result) []string {
	got := names(res.FileList)
	slices.Sort(got)
	return got
}

// With --dirs (-d) and no -r, a folder named with a trailing slash, or ".",
// is sent with its own entries one level deep, and any other folder as an
// entry only. gorsync's sender sent only the folder itself for "sub/", so a
// one-folder listing of a gorsync daemon read as empty, with no error.
func TestDirsListsOneLevelLikeStockRsync(t *testing.T) {
	src := t.TempDir()
	for _, p := range []string{"a", "sub/b", "sub/.dot", "sub/deep/c"} {
		full := filepath.Join(src, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := exec.LookPath("rsync")
	stock := err == nil
	flags := []string{"-n", "-d"}
	for req, want := range map[string][]string{
		"sub/": {".", ".dot", "b", "deep"},
		"./":   {".", "a", "sub"},
		".":    {".", "a", "sub"},
		"sub":  {"sub"},
	} {
		if got := sortedNames(listGorsync(t, src, flags, req)); !slices.Equal(got, want) {
			t.Errorf("gorsync -n -d %s: file list %q, want %q", req, got, want)
		}
		if !stock {
			continue
		}
		if got := sortedNames(listStock(t, src, flags, req)); !slices.Equal(got, want) {
			t.Errorf("stock rsync -n -d %s: file list %q, want %q", req, got, want)
		}
	}
}

// A folder the sender cannot read, named for its entries, must not read as
// empty: the list ends with the io_error flag set.
func TestDirsUnreadableFolderSetsIOError(t *testing.T) {
	src := unreadableTree(t)
	res := listGorsync(t, src, []string{"-n", "-d"}, "locked/")
	if res.IOErrors&1 == 0 {
		t.Errorf("gorsync -n -d locked/: IOErrors = %d, want bit 1 set; file list %q", res.IOErrors, names(res.FileList))
	}
}
