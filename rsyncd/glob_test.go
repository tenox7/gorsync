package rsyncd_test

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// globTree holds names that patterns can be mistaken for. w/ holds many
// names for the wildcard rules.
var globTree = []string{
	"d[1]/f1", "d1/f2", "a*b/f3", "axb/f4", `q\[z/f5`, "q[z/f6",
	"old mod/x1", "old/x2", "x", "Movie [1080p].mkv", "Movie 1.mkv",
	"trail /t", " lead/l",
	"w/a", "w/abc", "w/abd", "w/ab", "w/axxb", "w/axxbc", "w/notes.txt",
	"w/*", "w/axb", "w/a*b", "w/d[1]", "w/d1", `w/q\[z`, "w/q[z", `w/a\`,
	"w/ ", "w/old mod", "w/b", "w/d", "w/]", "w/-", "w/m", "w/7", "w/Q",
	"w/f", "w/!", "w/:", "w/[", "w/x[", "w/.hidden",
}

// globPatterns are asked for under w/, against stock rsync and gorsyncd.
var globPatterns = []string{
	"*", "**", "a*b", "*.txt", "?", "a?c", `\*`, `a\*b`, `d\[1\]`,
	`q\\\[z`, "d[1]", "[ ]", "old[ ]mod", "[abc]", "[!abc]", "[^abc]",
	"[a-c]", "[]]", "[]a]", "[!]]", "[a-]", "[-a]", "[a-c-e]", `[\]]`,
	`[\-]`, `[a-\z]`, "[[:digit:]]", "[[:alpha:][:digit:]]",
	"[![:space:]]", "[[:upper:]]", "[[:xdigit:]]", "[[:punct:]]",
	"[[:nope:]]", "[[:alpha]", "[[:]", "[a", "[", "[!", "*[", ".*", "*d*",
}

func makeGlobTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range globTree {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startStockDaemon runs stock rsync as a daemon serving dir as the
// read-only module mod.
func startStockDaemon(t *testing.T, dir string) string {
	t.Helper()
	return startStockDaemonModule(t, dir, true)
}

func startStockDaemonModule(t *testing.T, dir string, readOnly bool) string {
	t.Helper()
	bin := rsynctest.TridgeOrGTFO(t, "compares with the stock daemon")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	l.Close()
	conf := filepath.Join(t.TempDir(), "rsyncd.conf")
	cfg := fmt.Sprintf("use chroot = no\nport = %s\n[mod]\n  path = %s\n  read only = %v\n", port, dir, readOnly)
	if err := os.WriteFile(conf, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--daemon", "--no-detach", "--config="+conf, "--port="+port, "--log-file="+filepath.Join(filepath.Dir(conf), "d.log"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return addr
		}
	}
	t.Fatal("stock rsync daemon did not come up")
	return ""
}

// daemonList dry-runs "-n -d" against remotePath on the daemon at addr and
// returns the names in the file list, sorted.
func daemonList(t *testing.T, addr, remotePath string) []string {
	t.Helper()
	client, err := rsyncclient.New([]string{"-n", "-d"}, rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)), rsyncclient.DontRestrict())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	res, err := client.RunDaemon(t.Context(), conn, remotePath, []string{t.TempDir()})
	if err != nil || res == nil || res.IOErrors != 0 || res.XferErrors != 0 {
		// Stock rsync fails a path it cannot find; gorsyncd sends the
		// io_error flag. Either way the list is not a listing.
		return []string{"<error>"}
	}
	var got []string
	for _, fi := range res.FileList {
		got = append(got, fi.Name)
	}
	slices.Sort(got)
	return got
}

// A daemon reads each path a client asks for as rsync does: parts holding
// *, ? or [ are patterns, a backslash makes a byte stand for itself, and
// " mod/" splits an argument. gorsyncd took every path literally, so a
// client that escapes names for stock rsync got "not found".
func TestDaemonArgsLikeStockRsync(t *testing.T) {
	dir := makeGlobTree(t)
	stock := startStockDaemon(t, dir)
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dir}})
	gor := net.JoinHostPort("localhost", ts.Port)

	reqs := []string{
		"d[1]/", `d\[1\]/`, "a*b/", `a\*b/`, `q\\\[z/`, "q[z/",
		"old mod/x", "old[ ]mod/", "Movie [1080p].mkv", `Movie[ ]\[1080p\].mkv`,
		"trail /", " lead/", "*", "nomatch[", "x",
	}
	for _, p := range globPatterns {
		reqs = append(reqs, "w/"+p)
	}
	for _, req := range reqs {
		want := daemonList(t, stock, "mod/"+req)
		if got := daemonList(t, gor, "mod/"+req); !slices.Equal(got, want) {
			t.Errorf("-n -d mod/%s: gorsyncd sent %q, stock rsync %q", req, got, want)
		}
	}
	// The cases this is for, spelled out.
	for req, want := range map[string][]string{
		`d\[1\]/`:               {".", "f1"},
		"old[ ]mod/":            {".", "x1"},
		`Movie[ ]\[1080p\].mkv`: {"Movie [1080p].mkv"},
		"trail /":               {".", "t"},
		" lead/":                {".", "l"},
	} {
		if got := daemonList(t, gor, "mod/"+req); !slices.Equal(got, want) {
			t.Errorf("-n -d mod/%s: gorsyncd sent %q, want %q", req, got, want)
		}
	}
	if t.Failed() {
		t.Logf("names in w/: %s", strings.Join(globTree, ", "))
	}
}
