package receiver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestPendingFileTempSuccess(t *testing.T) {
	root, dir := openRoot(t)
	p, err := newPendingFile(root, "f", outputMode{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAtomicallyReplace(); err != nil {
		t.Fatal(err)
	}
	if err := p.Cleanup(); err != nil {
		t.Fatalf("Cleanup after success returned error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("dest = %q, want %q", got, "hello")
	}
	assertNoTempFiles(t, dir)
}

func TestPendingFileTempCleanupRemovesTemp(t *testing.T) {
	root, dir := openRoot(t)
	p, err := newPendingFile(root, "f", outputMode{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := p.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after Cleanup without KeepPartial: err=%v", err)
	}
	assertNoTempFiles(t, dir)
}

func TestPendingFileKeepPartialRenamesToDest(t *testing.T) {
	root, dir := openRoot(t)
	p, err := newPendingFile(root, "f", outputMode{KeepPartial: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := p.Cleanup(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatalf("dest should contain the partial: %v", err)
	}
	if string(got) != "partial" {
		t.Errorf("dest = %q, want %q", got, "partial")
	}
	assertNoTempFiles(t, dir)
}

func TestPendingFilePartialDir(t *testing.T) {
	root, dir := openRoot(t)
	p, err := newPendingFile(root, "f", outputMode{
		KeepPartial: true,
		PartialDir:  ".rsync-partial",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := p.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); !os.IsNotExist(err) {
		t.Errorf("dest should not exist (partial went to PartialDir): err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".rsync-partial", "f"))
	if err != nil {
		t.Fatalf("PartialDir/f should contain the partial: %v", err)
	}
	if string(got) != "partial" {
		t.Errorf("PartialDir/f = %q, want %q", got, "partial")
	}
}

func TestPendingFileInplaceWritesDirectly(t *testing.T) {
	root, dir := openRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("OLDOLDOLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := newPendingFile(root, "f", outputMode{Inplace: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("NEW")); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAtomicallyReplace(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Errorf("dest = %q, want %q (inplace must truncate trailing bytes)", got, "NEW")
	}
	assertNoTempFiles(t, dir)
}

func TestPendingFileInplaceCleanupKeepsPartial(t *testing.T) {
	root, dir := openRoot(t)
	p, err := newPendingFile(root, "f", outputMode{Inplace: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("PART")); err != nil {
		t.Fatal(err)
	}
	if err := p.Cleanup(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatalf("dest should contain inplace partial: %v", err)
	}
	if string(got) != "PART" {
		t.Errorf("dest = %q, want %q", got, "PART")
	}
}

// assertNoTempFiles fails the test if any of our hidden temp file pattern
// (.<name>.<digits>) is left behind in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && strings.Contains(name, ".") {
			parts := strings.Split(name, ".")
			last := parts[len(parts)-1]
			if last != "" && allDigits(last) {
				t.Errorf("temp file leaked: %s", name)
			}
		}
	}
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
