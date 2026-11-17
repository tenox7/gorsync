package rsyncclient_test

import (
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/internal/testlogger"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"
)

// WithSendOnly: a sending client sends the paths it is given and the
// folders above them, as rsync's --files-from, and nothing else of the
// folder. A caller that knows which files differ sends those alone.

// onlyTree makes a folder with files in and out of the list below.
func onlyTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for _, rel := range []string{"a.txt", "d/b.txt", "d/c.txt", "d/e/f.txt", "g/h.txt", "keep/x.txt"} {
		full := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return src
}

// tree lists root: folders end in /.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func pushOnlyStock(t *testing.T, src, dest string, only []string) *rsyncclient.Result {
	t.Helper()
	client, err := rsyncclient.New([]string{"-r", "-t"}, rsyncclient.WithSender(), rsyncclient.WithSendOnly(only),
		rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)))
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
	cmd.Wait() // exits 23 when the list says a path was not there
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func pushOnlyGorsyncd(t *testing.T, src, dest string, only []string) *rsyncclient.Result {
	t.Helper()
	ts := rsynctest.New(t, []rsyncd.Module{{Name: "mod", Path: dest, Writable: true}})
	client, err := rsyncclient.New([]string{"-r", "-t"}, rsyncclient.WithSender(), rsyncclient.WithSendOnly(only),
		rsyncclient.WithStdout(io.Discard), rsyncclient.WithStderr(testlogger.New(t)), rsyncclient.DontRestrict())
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

var onlyPushes = map[string]func(*testing.T, string, string, []string) *rsyncclient.Result{
	"stock rsync": pushOnlyStock,
	"gorsyncd":    pushOnlyGorsyncd,
}

func TestSendOnlySendsTheListedPaths(t *testing.T) {
	for name, push := range onlyPushes {
		t.Run(name, func(t *testing.T) {
			src, dest := onlyTree(t), t.TempDir()
			// keep is listed as a folder: it goes without keep/x.txt.
			res := push(t, src, dest, []string{"d/b.txt", "d/e/f.txt", "empty", "keep"})
			want := []string{"d/", "d/b.txt", "d/e/", "d/e/f.txt", "empty/", "keep/"}
			if got := tree(t, dest); !slices.Equal(got, want) {
				t.Errorf("the destination holds %q, want %q", got, want)
			}
			if res.IOErrors != 0 {
				t.Errorf("IOErrors = %d, want 0", res.IOErrors)
			}
			if b, err := os.ReadFile(filepath.Join(dest, "d", "e", "f.txt")); err != nil || string(b) != "d/e/f.txt" {
				t.Errorf("d/e/f.txt = %q, %v", b, err)
			}
		})
	}
}

// A listed path that is not there is an error of the run, as with rsync's
// --files-from: the run must not look complete.
func TestSendOnlyCountsAListedPathThatIsNotThere(t *testing.T) {
	for name, push := range onlyPushes {
		t.Run(name, func(t *testing.T) {
			src, dest := onlyTree(t), t.TempDir()
			res := push(t, src, dest, []string{"d/b.txt", "gone.txt"})
			if res.IOErrors&1 == 0 {
				t.Errorf("IOErrors = %d, want bit 1 for gone.txt", res.IOErrors)
			}
			want := []string{"d/", "d/b.txt"}
			if got := tree(t, dest); !slices.Equal(got, want) {
				t.Errorf("the destination holds %q, want %q", got, want)
			}
		})
	}
}

// A file the walk leaves out does not stop the walk in its folder: files
// sorted after it still go.
func TestSendOnlyKeepsWalkingAFolder(t *testing.T) {
	for name, push := range onlyPushes {
		t.Run(name, func(t *testing.T) {
			src, dest := onlyTree(t), t.TempDir()
			res := push(t, src, dest, []string{"d/c.txt"}) // d/b.txt, left out, comes first
			want := []string{"d/", "d/c.txt"}
			if got := tree(t, dest); !slices.Equal(got, want) {
				t.Errorf("the destination holds %q, want %q", got, want)
			}
			if res.IOErrors != 0 {
				t.Errorf("IOErrors = %d, want 0", res.IOErrors)
			}
		})
	}
}
