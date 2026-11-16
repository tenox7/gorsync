// Package wildmatch matches one path part against an rsync wildcard
// pattern, as rsync does when a daemon expands the paths a client asks for.
package wildmatch

// Match reports whether name, one path part (it holds no slash), matches
// pattern under rsync's wildcard rules:
//
//   - '*' matches any run of bytes, empty too; so does '**'.
//   - '?' matches any one byte.
//   - '[...]' matches one byte of a set. A set holds bytes, ranges such
//     as a-z, and classes such as [:alpha:]; a '!' or '^' first negates it.
//     A ']' first in the set (after any '!' or '^') is a member, and so is
//     a '-' that cannot make a range.
//   - '\' makes the byte after it stand for itself, in a set too.
//   - Any other byte matches itself.
//
// A pattern rsync cannot use matches nothing: a set with no closing ']', a
// class rsync does not know, or a '\' at the end.
func Match(pattern, name string) bool {
	p, n := 0, 0
	star, starN := -1, 0 // the last '*' seen, and where its match ends
	for n < len(name) {
		if p < len(pattern) {
			switch c := pattern[p]; c {
			case '*':
				star, starN = p, n
				p++
				continue
			case '?':
				p++
				n++
				continue
			case '[':
				in, next, ok := matchSet(pattern, p, name[n])
				if !ok {
					return false
				}
				if in {
					p = next
					n++
					continue
				}
			case '\\':
				if p+1 == len(pattern) {
					return false
				}
				if pattern[p+1] == name[n] {
					p += 2
					n++
					continue
				}
			default:
				if c == name[n] {
					p++
					n++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		// The last '*' takes one more byte; match the rest from there.
		starN++
		p, n = star+1, starN
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchSet reports whether b is in the set that opens at pattern[i] (a
// '['), and the index just past the set. ok is false when rsync cannot use
// the set.
func matchSet(pattern string, i int, b byte) (in bool, next int, ok bool) {
	i++
	if i == len(pattern) {
		return false, 0, false
	}
	negate := pattern[i] == '!' || pattern[i] == '^'
	if negate {
		i++
	}
	matched := false
	prev := -1 // the member before, which a '-' can start a range from
	for first := true; ; first = false {
		if i == len(pattern) {
			return false, 0, false
		}
		c := pattern[i]
		if c == ']' && !first {
			return matched != negate, i + 1, true
		}
		switch {
		case c == '\\':
			i++
			if i == len(pattern) {
				return false, 0, false
			}
			c = pattern[i]
			matched = matched || b == c
			prev = int(c)
		case c == '-' && prev >= 0 && i+1 < len(pattern) && pattern[i+1] != ']':
			i++
			hi := pattern[i]
			if hi == '\\' {
				i++
				if i == len(pattern) {
					return false, 0, false
				}
				hi = pattern[i]
			}
			matched = matched || (b >= byte(prev) && b <= hi)
			prev = -1
		case c == '[' && i+1 < len(pattern) && pattern[i+1] == ':':
			end := i + 2
			for end < len(pattern) && pattern[end] != ']' {
				end++
			}
			if end == len(pattern) {
				return false, 0, false
			}
			if end-1 < i+2 || pattern[end-1] != ':' {
				// No ":]": the '[' is a member; go on at the ':'.
				matched = matched || b == '['
				prev = '['
				break
			}
			in, known := inClass(pattern[i+2:end-1], b)
			if !known {
				return false, 0, false
			}
			matched = matched || in
			prev = -1
			i = end
		default:
			matched = matched || b == c
			prev = int(c)
		}
		i++
	}
}

// inClass reports whether b is in the named class; known is false for a
// name rsync does not know. Bytes from 0x80 up are in no class.
func inClass(name string, b byte) (in, known bool) {
	lower := b >= 'a' && b <= 'z'
	upper := b >= 'A' && b <= 'Z'
	digit := b >= '0' && b <= '9'
	graph := b > ' ' && b < 0x7f
	switch name {
	case "alnum":
		return lower || upper || digit, true
	case "alpha":
		return lower || upper, true
	case "blank":
		return b == ' ' || b == '\t', true
	case "cntrl":
		return b < ' ' || b == 0x7f, true
	case "digit":
		return digit, true
	case "graph":
		return graph, true
	case "lower":
		return lower, true
	case "print":
		return graph || b == ' ', true
	case "punct":
		return graph && !lower && !upper && !digit, true
	case "space":
		return b == ' ' || (b >= '\t' && b <= '\r'), true
	case "upper":
		return upper, true
	case "xdigit":
		return digit || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F'), true
	}
	return false, false
}
