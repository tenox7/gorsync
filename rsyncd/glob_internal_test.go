package rsyncd

import (
	"slices"
	"testing"
	"testing/fstest"
)

func TestExpandArgs(t *testing.T) {
	fsys := fsArgFS{fstest.MapFS{
		"d[1]/f":     {Data: []byte("1")},
		"d1/f":       {Data: []byte("2")},
		"old mod/x":  {Data: []byte("3")},
		"x":          {Data: []byte("4")},
		"sub/a.txt":  {Data: []byte("5")},
		"sub/b.txt":  {Data: []byte("6")},
		"file[1]":    {Data: []byte("7")},
		"plain/dir/": {},
	}}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"mod"}, []string{"."}},
		{[]string{"mod/"}, []string{"/"}},
		{[]string{"mod/sub/"}, []string{"/sub/"}},
		{[]string{`mod/d\[1\]/`}, []string{"/d[1]/"}},
		{[]string{"mod/d[1]/"}, []string{"/d1/"}},
		{[]string{"mod/old mod/x"}, []string{"/old", "x"}},
		{[]string{"mod/old[ ]mod/x"}, []string{"/old mod/x"}},
		{[]string{"mod/sub/*.txt"}, []string{"/sub/a.txt", "/sub/b.txt"}},
		{[]string{"mod/file[1]/x"}, []string{"/file[1]/x"}},
		{[]string{"mod/nomatch[/x"}, []string{"/nomatch[/x"}},
		{[]string{"mod/../*"}, []string{"/../*"}},
		{[]string{"mod/a mod/"}, []string{"/a"}},
		{[]string{"mod/trail /"}, []string{"/trail /"}},
	} {
		if got := expandArgs(fsys, "mod", tc.args); !slices.Equal(got, tc.want) {
			t.Errorf("expandArgs(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
	if got := expandArgs(nil, "mod", []string{"mod/d[1]/"}); !slices.Equal(got, []string{"/d[1]/"}) {
		t.Errorf("expandArgs without a module folder = %q, want the path as written", got)
	}
}
