package sshconf

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxIncludeDepth matches OpenSSH's own limit on nested Include directives.
const maxIncludeDepth = 16

// expandPath resolves the '~' prefix and makes relative Include paths relative
// to base, which is ~/.ssh for a user configuration. This is what ssh does; the
// directory of the including file is deliberately not used.
func expandPath(p, base, home string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	// A Windows path such as C:\x is absolute but filepath.IsAbs already
	// covers it; a leading slash on Windows is treated as rooted too.
	if strings.HasPrefix(p, "/") {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// globInclude expands one Include argument into the concrete files it names,
// in the sorted order ssh uses. A pattern that matches nothing is not an error.
func globInclude(arg, base, home string) []string {
	pat := expandPath(arg, base, home)
	if pat == "" {
		return nil
	}
	if !strings.ContainsAny(pat, "*?[") {
		if st, err := os.Stat(pat); err == nil && !st.IsDir() {
			return []string{pat}
		}
		return nil
	}
	matches, err := filepath.Glob(pat)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && !st.IsDir() {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
