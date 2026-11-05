package sender

import (
	"io"
	"strings"
	"testing"

	"github.com/gokrazy/rsync/internal/rsyncwire"
)

func buildList(t *testing.T, rules ...string) *filterRuleList {
	t.Helper()
	var l filterRuleList
	for _, r := range rules {
		fr, err := parseFilter(r)
		if err != nil {
			t.Fatalf("parseFilter(%q): %v", r, err)
		}
		l.addRule(fr)
	}
	return &l
}

func TestExcludedSurgicalFileDelete(t *testing.T) {
	l := buildList(t, "+ /foo", "- *")
	cases := []struct {
		name string
		want bool
	}{
		{"foo", false},
		{"bar", true},
		{"baz", true},
		{"subdir/foo", true},
	}
	for _, c := range cases {
		if got := l.Excluded(c.name); got != c.want {
			t.Errorf("Excluded(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExcludedSurgicalDirDelete(t *testing.T) {
	l := buildList(t, "+ /foo", "+ /foo/***", "- *")
	cases := []struct {
		name string
		want bool
	}{
		{"foo", false},
		{"foo/inner", false},
		{"bar", true},
		{"bar/inner", true},
	}
	for _, c := range cases {
		if got := l.Excluded(c.name); got != c.want {
			t.Errorf("Excluded(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRecvFilterListAcceptsWildcards reads a wire-encoded filter list with the
// rules a surgical --delete sends. Upstream's RecvFilterList rejects wildcard
// rules ("wildcard filter rules not yet implemented"); this fork matches them,
// so that rejection must not come back with an upstream merge.
func TestRecvFilterListAcceptsWildcards(t *testing.T) {
	var wire rsyncwire.Buffer
	for _, rule := range []string{"+ /foo", "+ /foo/***", "- *"} {
		wire.WriteInt32(int32(len(rule)))
		wire.WriteString(rule)
	}
	wire.WriteInt32(0) // end of list
	l, err := RecvFilterList(&rsyncwire.Conn{
		Reader: io.NopCloser(strings.NewReader(wire.String())),
		Writer: nopClose(io.Discard),
	})
	if err != nil {
		t.Fatalf("RecvFilterList: %v", err)
	}
	cases := []struct {
		name string
		want bool
	}{
		{"foo", false},
		{"foo/inner", false},
		{"bar", true},
		{"bar/inner", true},
	}
	for _, c := range cases {
		if got := l.Excluded(c.name); got != c.want {
			t.Errorf("Excluded(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExcludedEmptyList(t *testing.T) {
	l := buildList(t)
	if l.Excluded("anything") {
		t.Errorf("Excluded on empty rule list should be false")
	}
}

func TestMatchesAnchored(t *testing.T) {
	fr, _ := parseFilter("- /foo")
	var l filterRuleList
	l.addRule(fr)
	if !fr.matches("foo") {
		t.Errorf("/foo should match top-level foo")
	}
	if fr.matches("sub/foo") {
		t.Errorf("/foo should NOT match nested sub/foo (anchored)")
	}
	if fr.matches("foo/sub") {
		t.Errorf("/foo should NOT match foo/sub (anchored, no trailing slash)")
	}
}

func TestMatchesUnanchored(t *testing.T) {
	fr, _ := parseFilter("- foo")
	var l filterRuleList
	l.addRule(fr)
	if !fr.matches("foo") {
		t.Errorf("foo should match foo")
	}
	if !fr.matches("a/b/foo") {
		t.Errorf("unanchored foo should match at any depth (basename)")
	}
	if fr.matches("foobar") {
		t.Errorf("unanchored foo should not match foobar")
	}
}

func TestMatchesWildcardStar(t *testing.T) {
	fr, _ := parseFilter("- *")
	var l filterRuleList
	l.addRule(fr)
	if !fr.matches("anything") {
		t.Errorf("* should match anything")
	}
	if !fr.matches("a/b/c") {
		t.Errorf("* should match basename of nested path")
	}
}
