package rsyncd

import (
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/gokrazy/rsync/internal/wildmatch"
)

// argFS is what expanding the path arguments of a request reads from a
// module: the names in a folder, and what a path is.
type argFS interface {
	ReadDir(name string) ([]fs.DirEntry, error)
	Stat(name string) (fs.FileInfo, error)
}

// rootArgFS reads a module folder through an os.Root, as the sender and
// the receiver do, so a pattern cannot reach outside the module.
type rootArgFS struct{ root *os.Root }

func (r rootArgFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (r rootArgFS) Stat(name string) (fs.FileInfo, error) { return r.root.Stat(name) }

type fsArgFS struct{ fsys fs.FS }

func (f fsArgFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(f.fsys, name) }
func (f fsArgFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(f.fsys, name) }

// moduleArgFS opens module for expanding arguments; nil when it cannot,
// and then the arguments are taken as written.
func moduleArgFS(module *Module) (argFS, func()) {
	if module.FS != nil {
		return fsArgFS{module.FS}, func() {}
	}
	root, err := os.OpenRoot(module.Path)
	if err != nil {
		return nil, func() {}
	}
	return rootArgFS{root}, func() { root.Close() }
}

// expandArgs turns the path arguments of a daemon request into the paths
// to serve, the way a stock rsync daemon does (rsync io.c read_args,
// util1.c glob_expand_module and glob_match):
//
//   - The module name in front is dropped.
//   - An argument is split at each " <module>/": "old mod/x" asks for
//     "old" and "x". This is rsync's old way to ask for several paths.
//   - A part of a path that holds *, ? or [ is a pattern (see
//     wildmatch.Match), matched against the names in the folder before it;
//     each name it matches gives a path, and a part before the last must
//     be a folder. Other parts are names as written, '\' included.
//   - A path whose patterns match nothing is used as written.
//
// So a client that wants a name holding *, ? or [, or ending in
// " <module>", sends it as a pattern that matches only that name.
func expandArgs(fsys argFS, module string, args []string) []string {
	var out []string
	for _, arg := range args {
		rest := strings.TrimPrefix(arg, module)
		if rest == "" {
			out = append(out, ".")
			continue
		}
		pieces := strings.Split(rest, " "+module+"/")
		if len(pieces) > 1 && pieces[len(pieces)-1] == "" {
			pieces = pieces[:len(pieces)-1]
		}
		for _, piece := range pieces {
			if piece == "" {
				piece = "."
			}
			out = append(out, globExpand(fsys, piece)...)
		}
	}
	return out
}

// globExpand expands the patterns in path p; see expandArgs.
func globExpand(fsys argFS, p string) []string {
	if fsys == nil || !strings.ContainsAny(p, "*?[") {
		return []string{p}
	}
	parts := strings.Split(p, "/")
	if slices.Contains(parts, "..") {
		return []string{p}
	}
	var out []string
	// match matches parts[i:] under dir, the module path matched so far
	// ("." for the module root); built is the argument text so far.
	var match func(dir, built string, i int)
	match = func(dir, built string, i int) {
		if i == len(parts) {
			out = append(out, built)
			return
		}
		part, last := parts[i], i == len(parts)-1
		next := func(name string) {
			d := name
			if dir != "." {
				d = dir + "/" + name
			}
			b := name
			if i > 0 {
				b = built + "/" + name
			}
			match(d, b, i+1)
		}
		isDir := func(name string) (ok, exists bool) {
			d := name
			if dir != "." {
				d = dir + "/" + name
			}
			fi, err := fsys.Stat(d)
			if err != nil {
				return false, false
			}
			return fi.IsDir(), true
		}
		switch {
		case part == "" || part == ".":
			// From a leading, doubled or trailing slash, or "./".
			b := part
			if i > 0 {
				b = built + "/" + part
			}
			match(dir, b, i+1)
		case strings.ContainsAny(part, "*?["):
			entries, err := fsys.ReadDir(dir)
			if err != nil {
				return
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			slices.Sort(names)
			for _, name := range names {
				if !wildmatch.Match(part, name) {
					continue
				}
				if dirOK, exists := isDir(name); !exists || (!last && !dirOK) {
					continue
				}
				next(name)
			}
		default:
			if !last {
				if dirOK, _ := isDir(part); !dirOK {
					return
				}
			}
			next(part)
		}
	}
	match(".", "", 0)
	if len(out) == 0 {
		return []string{p}
	}
	return out
}
