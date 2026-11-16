package wildmatch

import "testing"

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"", "", true},
		{"", "a", false},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"*", "", true},
		{"*", "anything", true},
		{"**", "x", true},
		{"a*b", "ab", true},
		{"a*b", "axxb", true},
		{"a*b", "axxbc", false},
		{"*.txt", "notes.txt", true},
		{"*a*a*a*a*a*a*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"?", "", false},
		{"?", "x", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{`\*`, "*", true},
		{`\*`, "x", false},
		{`a\*b`, "axb", false},
		{`a\*b`, "a*b", true},
		{`d\[1\]`, "d[1]", true},
		{`d\[1\]`, "d1", false},
		{`q\\\[z`, `q\[z`, true},
		{`q\\\[z`, "q[z", false},
		{`\x`, "x", true},
		{`a\`, "a", false},
		{`a\`, `a\`, false},
		{"d[1]", "d1", true},
		{"d[1]", "d[1]", false},
		{"[ ]", " ", true},
		{"old[ ]mod", "old mod", true},
		{"[abc]", "b", true},
		{"[abc]", "d", false},
		{"[!abc]", "d", true},
		{"[!abc]", "a", false},
		{"[^abc]", "d", true},
		{"[a-c]", "b", true},
		{"[a-c]", "d", false},
		{"[]]", "]", true},
		{"[]a]", "a", true},
		{"[!]]", "]", false},
		{"[!]]", "x", true},
		{"[a-]", "-", true},
		{"[-a]", "-", true},
		{"[a-c-e]", "-", true},
		{"[a-c-e]", "d", false},
		{`[\]]`, "]", true},
		{`[\-]`, "-", true},
		{`[a-\z]`, "m", true},
		{"[[:digit:]]", "7", true},
		{"[[:digit:]]", "x", false},
		{"[[:alpha:][:digit:]]", "x", true},
		{"[![:space:]]", " ", false},
		{"[[:upper:]]", "Q", true},
		{"[[:xdigit:]]", "f", true},
		{"[[:punct:]]", "!", true},
		{"[[:nope:]]", "x", false},
		{"[[:alpha]", ":", true},
		{"[[:alpha]", "[", true},
		{"[[:alpha]", "x", false},
		{"[[:]", ":", true},
		{"[a", "a", false},
		{"[", "[", false},
		{"[!", "x", false},
		{"*[", "x[", false},
		{"[\xe4]", "\xe4", true},
		{"[[:alpha:]]", "\xe4", false},
	} {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// Patterns come from the network: no pattern may panic, and a name with a
// backslash before each byte matches itself.
func FuzzMatch(f *testing.F) {
	for _, s := range []string{"", "*", "[a-c]", "[[:alpha:]]", `\`, "[!]]", "[[:]", "a*b?c[d-f]"} {
		f.Add(s, "abc")
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		Match(pattern, name)
		var esc []byte
		for i := 0; i < len(name); i++ {
			esc = append(esc, '\\', name[i])
		}
		if !Match(string(esc), name) {
			t.Errorf("Match(%q, %q) = false", esc, name)
		}
	})
}
