package sshconf

import "strings"

// matchPattern implements ssh's pattern matching: '*' matches any run of
// characters, '?' matches exactly one, everything else is literal and compared
// case-insensitively. Negation is handled by the caller.
func matchPattern(pat, s string) bool {
	if pat == "*" {
		return true
	}
	return globMatch(strings.ToLower(pat), strings.ToLower(s))
}

// globMatch is an iterative matcher with linear backtracking on '*', which
// keeps pathological patterns from blowing up.
func globMatch(pat, s string) bool {
	var (
		p, i      int
		star      = -1
		starMatch int
	)
	for i < len(s) {
		switch {
		case p < len(pat) && (pat[p] == '?' || pat[p] == s[i]):
			p++
			i++
		case p < len(pat) && pat[p] == '*':
			star = p
			starMatch = i
			p++
		case star >= 0:
			p = star + 1
			starMatch++
			i = starMatch
		default:
			return false
		}
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}

// MatchAny reports whether host matches any of the given patterns, honouring
// leading '!' negation the way a Host line does.
func MatchAny(patterns []string, host string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		if matchPattern(strings.TrimPrefix(p, "!"), host) {
			if neg {
				return false
			}
			matched = true
		}
	}
	return matched
}
