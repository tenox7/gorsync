package rsyncclient_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/rsyncclient"
)

func linkTargets(list []rsync.FileInfo) map[string]string {
	got := map[string]string{}
	for _, fi := range list {
		got[fi.Name] = fi.LinkTarget
	}
	return got
}

// A caller that mirrors links (sc, 2026-11-20) needs each link's target
// from the file list. The receiver read it with -l, and FileInfo had no
// field for it. Now LinkTarget holds it, in Result.FileList and in the
// file list callback: from gorsync's sender and from stock rsync's, the
// text as the link holds it, also for a link to nothing. Without -l the
// sender sends none.
func TestFileListGivesLinkTargets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{"rel": "a.txt", "dangling": "/nonexistent/x", "odd": "sp ace\ttab"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	want := maps.Clone(links)
	want["a.txt"], want["."] = "", ""

	var seen []rsync.FileInfo
	res := listGorsync(t, dir, []string{"-n", "-d", "-l"}, "./", rsyncclient.WithFileListCallback(func(fi rsync.FileInfo) { seen = append(seen, fi) }))
	if got := linkTargets(res.FileList); !maps.Equal(got, want) {
		t.Errorf("gorsyncd, -l: targets %q, want %q", got, want)
	}
	if got := linkTargets(seen); !maps.Equal(got, want) {
		t.Errorf("gorsyncd, -l: the file list callback gave the targets %q, want %q", got, want)
	}
	res = listGorsync(t, dir, []string{"-n", "-d"}, "./")
	for name, target := range linkTargets(res.FileList) {
		if target != "" {
			t.Errorf("gorsyncd, no -l: %s has the target %q", name, target)
		}
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("no rsync here to compare with")
	}
	res = listStock(t, dir, []string{"-n", "-d", "-l"}, "./")
	if got := linkTargets(res.FileList); !maps.Equal(got, want) {
		t.Errorf("stock rsync, -l: targets %q, want %q", got, want)
	}
}
