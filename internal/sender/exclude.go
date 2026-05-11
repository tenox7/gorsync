package sender

import (
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/gokrazy/rsync/internal/rsyncwire"
)

type filterRuleList struct {
	Filters []*filterRule
}

// exclude.c:add_rule
func (l *filterRuleList) addRule(fr *filterRule) {
	if strings.HasSuffix(fr.pattern, "/") {
		fr.flag |= filtruleDirectory
		fr.pattern = strings.TrimSuffix(fr.pattern, "/")
	}
	if strings.ContainsFunc(fr.pattern, func(r rune) bool {
		return r == '*' || r == '[' || r == '?'
	}) {
		fr.flag |= filtruleWild
	}
	l.Filters = append(l.Filters, fr)
}

// exclude.c:check_filter
func (l *filterRuleList) matches(name string) bool {
	for _, fr := range l.Filters {
		if fr.matches(name) {
			return true
		}
	}
	return false
}

// Excluded reports whether name is protected from deletion by the first
// matching rule (i.e. the rule is an exclude). Used by the receiver when
// walking the destination tree under --delete: paths matched by an exclude
// rule must be preserved. An include rule that matches first means the path
// is eligible for deletion per the normal source-fileList check.
func (l *filterRuleList) Excluded(name string) bool {
	for _, fr := range l.Filters {
		if !fr.matches(name) {
			continue
		}
		return fr.flag&filtruleInclude == 0
	}
	return false
}

// exclude.c:recv_filter_list
func RecvFilterList(c *rsyncwire.Conn) (*filterRuleList, error) {
	var l filterRuleList
	const exclusionListEnd = 0
	for {
		length, err := c.ReadInt32()
		if err != nil {
			return nil, err
		}
		if length == exclusionListEnd {
			break
		}
		line := make([]byte, length)
		if _, err := io.ReadFull(c.Reader, line); err != nil {
			return nil, err
		}
		fr, err := parseFilter(string(line))
		if err != nil {
			return nil, err
		}
		l.addRule(fr)
	}
	return &l, nil
}

const (
	filtruleInclude = 1 << iota
	filtruleClearList
	filtruleDirectory
	filtruleWild
)

type filterRule struct {
	flag    int
	pattern string
}

// exclude.c:rule_matches
func (fr *filterRule) matches(name string) bool {
	pat := fr.pattern
	anchored := strings.HasPrefix(pat, "/")
	if anchored {
		pat = strings.TrimPrefix(pat, "/")
	}
	candidate := name
	if !anchored && !strings.ContainsRune(pat, '/') {
		candidate = filepath.Base(name)
	}
	if fr.flag&filtruleWild != 0 {
		ok, _ := path.Match(pat, candidate)
		return ok
	}
	return pat == candidate
}

// exclude.c:parse_filter_str / exclude.c:parse_rule_tok
func parseFilter(line string) (*filterRule, error) {
	rule := new(filterRule)

	// We only support what rsync calls XFLG_OLD_PREFIXES
	if strings.HasPrefix(line, "- ") {
		// clear include flag
		rule.flag &= ^filtruleInclude
		line = strings.TrimPrefix(line, "- ")
	} else if strings.HasPrefix(line, "+ ") {
		// set include flag
		rule.flag |= filtruleInclude
		line = strings.TrimPrefix(line, "+ ")
	} else if strings.HasPrefix(line, "!") {
		// set clear_list flag
		rule.flag |= filtruleClearList
	}

	rule.pattern = line

	return rule, nil
}
