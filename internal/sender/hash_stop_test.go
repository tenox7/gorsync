package sender

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hashGate is a FileSource over one file on disk. Its first Open is the
// send's. The second, the md4 hash's, gives a file whose first Read waits
// for release, and which counts what it reads and says when it is closed.
type hashGate struct {
	path    string
	opens   atomic.Int32
	release chan struct{}
	read    atomic.Int64
	closed  chan struct{}
	once    sync.Once
}

func (s *hashGate) FS() fs.FS                            { return os.DirFS(filepath.Dir(s.path)) }
func (s *hashGate) Readlink(name string) (string, error) { return "", fs.ErrInvalid }
func (s *hashGate) Close() error                         { return nil }

func (s *hashGate) Open(string) (File, error) {
	f, err := os.Open(s.path)
	if err != nil || s.opens.Add(1) == 1 {
		return f, err
	}
	return &gatedFile{File: f, s: s}, nil
}

type gatedFile struct {
	*os.File
	s    *hashGate
	once sync.Once
}

func (f *gatedFile) Read(p []byte) (int, error) {
	f.once.Do(func() { <-f.s.release })
	n, err := f.File.Read(p)
	f.s.read.Add(int64(n))
	return n, err
}

func (f *gatedFile) Close() error {
	defer f.s.once.Do(func() { close(f.s.closed) })
	return f.File.Close()
}

// failAfter takes n bytes, then fails each write, as a link that died.
type failAfter struct{ n int }

func (w *failAfter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		w.n = 0
		return 0, errors.New("the link is down")
	}
	w.n -= len(p)
	return len(p), nil
}

// sendFile hashes the file in a second goroutine, which reads it a second
// time. A send that failed left that goroutine reading the file to its
// end: each try of a send over a link that keeps dying read the whole
// source once more, beside the next try. Now a failed send stops it at
// its next read.
func TestAFailedSendStopsItsHash(t *testing.T) {
	const size = 16 << 20
	p := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &hashGate{path: p, release: make(chan struct{}), closed: make(chan struct{})}
	st := fuzzTransfer(t, nil)
	st.Conn.Writer = nopClose(&failAfter{n: 64}) // the index, the sum head, a token length
	if err := st.sendFile(0, file{source: src, path: "big", regular: true}); err == nil {
		t.Fatal("the send went through a link that died")
	}
	close(src.release)
	select {
	case <-src.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the hash still reads the file 10s after its send failed")
	}
	if n := src.read.Load(); n > 2*readBufSize {
		t.Errorf("the hash read %d of the file's %d bytes after its send failed", n, size)
	}
}
