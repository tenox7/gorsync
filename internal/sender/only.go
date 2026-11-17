package sender

import (
	"path"
	"sort"
)

// onlySet is what a sender limited to a list of paths sends: the listed
// paths and every folder above one, as rsync's --files-from. A listed
// folder goes as a folder; what it holds goes only when that is listed too.
type onlySet struct {
	listed  map[string]bool // by file list name; true once the walk saw it
	parents map[string]bool
}

// newOnlySet returns nil for a nil list: everything goes.
func newOnlySet(paths []string) *onlySet {
	if paths == nil {
		return nil
	}
	o := &onlySet{listed: map[string]bool{}, parents: map[string]bool{}}
	for _, p := range paths {
		p = path.Clean(p)
		o.listed[p] = false
		// A folder already there has its own folders above it there too.
		for d := path.Dir(p); d != "." && d != "/" && !o.parents[d]; d = path.Dir(d) {
			o.parents[d] = true
		}
	}
	return o
}

// allows says whether the entry with file list name name goes, and, when it
// does not, whether it is a folder the walk need not go into.
func (o *onlySet) allows(name string, dir bool) (send, prune bool) {
	if o == nil || name == "." {
		return true, false
	}
	if _, ok := o.listed[name]; ok {
		o.listed[name] = true
		return true, false
	}
	if dir {
		if o.parents[name] {
			return true, false
		}
		return false, true
	}
	return false, false
}

// missing are the listed paths the walk did not come to, sorted.
func (o *onlySet) missing() []string {
	if o == nil {
		return nil
	}
	var out []string
	for name, seen := range o.listed {
		if !seen {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
