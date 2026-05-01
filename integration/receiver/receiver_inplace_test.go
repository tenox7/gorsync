package receiver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gokrazy/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/rsyncd"
)

// TestReceiverInplace verifies that --inplace writes directly to the
// destination file, preserving the inode (no shadow temp file is left
// behind, and the destination's content is correct after delta-sync).
func TestReceiverInplace(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	dest := filepath.Join(tmp, "dest")
	destLarge := filepath.Join(dest, "large-data-file")

	headPattern := []byte{0x11}
	bodyPattern := []byte{0xbb}
	endPattern := []byte{0xee}
	rsynctest.WriteLargeDataFile(t, source, headPattern, bodyPattern, endPattern)
	// Set an explicit mtime so we can later set a distinctly-newer one,
	// otherwise os.WriteFile within the same second would leave mtimes
	// equal and the receiver's quickcheck would skip the file.
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(source, "large-data-file"), t1, t1); err != nil {
		t.Fatal(err)
	}

	srv := rsynctest.NewInMemory(t, rsyncd.Module{
		Name: "interop",
		Path: source,
	})

	args := []string{"-aH", "--inplace"}
	srv.RunClient(t, args, []string{dest})

	if err := rsynctest.DataFileMatches(destLarge, headPattern, bodyPattern, endPattern); err != nil {
		t.Fatalf("after first sync: %v", err)
	}
	assertNoLeakedTemps(t, dest)

	// Capture the file identity so we can later confirm --inplace re-used
	// it (no shadow file + rename, which would change the inode).
	st1, err := os.Stat(destLarge)
	if err != nil {
		t.Fatal(err)
	}

	// Modify the source and re-sync. Delta-sync should patch destLarge in
	// place rather than creating a shadow file.
	bodyPattern = []byte{0x66}
	rsynctest.WriteLargeDataFile(t, source, headPattern, bodyPattern, endPattern)
	t2 := t1.Add(24 * time.Hour)
	if err := os.Chtimes(filepath.Join(source, "large-data-file"), t2, t2); err != nil {
		t.Fatal(err)
	}

	srv.RunClient(t, args, []string{dest})

	if err := rsynctest.DataFileMatches(destLarge, headPattern, bodyPattern, endPattern); err != nil {
		t.Fatalf("after second sync: %v", err)
	}
	assertNoLeakedTemps(t, dest)

	st2, err := os.Stat(destLarge)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(st1, st2) {
		t.Errorf("--inplace should preserve the destination file identity (no rename)")
	}
}

// TestReceiverPartial verifies that a normal transfer with --partial still
// finishes cleanly (no leftover partials when nothing failed).
func TestReceiverPartial(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	dest := filepath.Join(tmp, "dest")

	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "hello"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := rsynctest.NewInMemory(t, rsyncd.Module{
		Name: "interop",
		Path: source,
	})

	srv.RunClient(t, []string{"-a", "--partial"}, []string{dest})

	got, err := os.ReadFile(filepath.Join(dest, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Errorf("dest content = %q, want %q", got, "world")
	}
	assertNoLeakedTemps(t, dest)
}

// assertNoLeakedTemps walks dir and fails the test if a hidden temp-style
// filename (.<name>.<digits>) is present.
func assertNoLeakedTemps(t *testing.T, dir string) {
	t.Helper()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, ".") {
			return nil
		}
		parts := strings.Split(base, ".")
		last := parts[len(parts)-1]
		if last == "" {
			return nil
		}
		allDigit := true
		for _, r := range last {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			t.Errorf("leaked temp file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
