// Package panicerr turns a panic into an error. A bug in this library, or
// in a callback a caller gave it, then fails the one run it happened in,
// not the program the library runs in: a program with other runs going,
// or a terminal to put back, would lose them all.
package panicerr

import (
	"fmt"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
)

// Error is a panic that Recover caught.
type Error struct {
	Value any    // what was passed to panic
	Where string // the file and line that panicked, if found
	Stack []byte // the stack of the goroutine that panicked
}

func (e *Error) Error() string {
	if e.Where == "" {
		return fmt.Sprintf("rsync: internal error: %v", e.Value)
	}
	return fmt.Sprintf("rsync: internal error: %v (%s)", e.Value, e.Where)
}

// Recover, deferred, turns a panic under way into an *Error in *err. Defer
// it itself, not from inside another deferred function: recover stops a
// panic only when the deferred function calls it.
func Recover(err *error) {
	if r := recover(); r != nil {
		*err = &Error{Value: r, Where: where(), Stack: debug.Stack()}
	}
}

// Call runs f and returns its error, or the panic it raised.
func Call(f func() error) (err error) {
	defer Recover(&err)
	return f()
}

// where finds the frame that panicked: the first one past the runtime's
// panic frames, as a folder, file and line.
func where() string {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	inRuntime := false
	for {
		f, more := frames.Next()
		switch {
		case strings.HasPrefix(f.Function, "runtime."):
			inRuntime = true
		case inRuntime:
			return fmt.Sprintf("%s:%d", path.Join(path.Base(path.Dir(f.File)), path.Base(f.File)), f.Line)
		}
		if !more {
			return ""
		}
	}
}
