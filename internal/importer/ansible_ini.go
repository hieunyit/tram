package importer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// iniGroup is one section of an Ansible INI inventory.
type iniGroup struct {
	name     string
	hosts    []string
	vars     map[string]string
	children []string
	parents  []string
}

// iniHost is a host line, with the variables written on that line.
type iniHost struct {
	name  string
	vars  map[string]string
	line  int
	group string
}

// parseINI reads the classic Ansible inventory format.
//
// Three section shapes exist and all three matter: `[g]` lists hosts, `[g:vars]`
// sets variables for every host in g and in its descendants, and `[g:children]`
// nests groups. tram turns that nesting into its own slash-separated group
// path, which is the whole reason the children sections are read at all.
func parseINI(data []byte, opt Options) (*Result, error) {
	res := &Result{}

	groups := map[string]*iniGroup{}
	order := []string{}
	group := func(name string) *iniGroup {
		g, ok := groups[name]
		if !ok {
			g = &iniGroup{name: name, vars: map[string]string{}}
			groups[name] = g
			order = append(order, name)
		}
		return g
	}

	var hosts []iniHost
	seen := map[string]int{} // host name -> index in hosts

	section, mode := "ungrouped", "hosts"
	group("ungrouped")

	for i, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.TrimSpace(strings.Trim(line, "[]"))
			section, mode = name, "hosts"
			if idx := strings.LastIndex(name, ":"); idx > 0 {
				section, mode = name[:idx], name[idx+1:]
			}
			group(section)
			continue
		}

		switch mode {
		case "vars":
			k, v, ok := splitVar(line)
			if !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("line %d: %q is not a key=value pair", i+1, line))
				continue
			}
			group(section).vars[k] = v

		case "children":
			child := strings.Fields(line)[0]
			g, c := group(section), group(child)
			g.children = append(g.children, child)
			c.parents = append(c.parents, section)

		default:
			fields := splitHostLine(line)
			if len(fields) == 0 {
				continue
			}
			names, err := expandRange(fields[0])
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("line %d: %v", i+1, err))
				continue
			}
			vars := map[string]string{}
			for _, f := range fields[1:] {
				if k, v, ok := splitVar(f); ok {
					vars[k] = v
				}
			}
			for _, name := range names {
				group(section).hosts = append(group(section).hosts, name)
				if idx, dup := seen[name]; dup {
					// A host listed in several groups is one machine. Merge the
					// variables rather than emitting it twice.
					for k, v := range vars {
						hosts[idx].vars[k] = v
					}
					continue
				}
				seen[name] = len(hosts)
				copied := map[string]string{}
				for k, v := range vars {
					copied[k] = v
				}
				hosts = append(hosts, iniHost{name: name, vars: copied, line: i + 1, group: section})
			}
		}
	}

	paths := groupPaths(groups, order, opt.FlatGroups)

	for _, h := range hosts {
		// Variables come from the group chain first, outermost to innermost,
		// then from the host line, so the most specific setting wins.
		merged := map[string]string{}
		for _, gname := range membership(groups, order, h.name) {
			for _, anc := range ancestry(groups, gname) {
				for k, v := range groups[anc].vars {
					merged[k] = v
				}
			}
		}
		// [all:vars] applies everywhere, but only where nothing more specific
		// has spoken. Most inventories never declare it.
		if all := groups["all"]; all != nil {
			for k, v := range all.vars {
				if _, set := merged[k]; !set {
					merged[k] = v
				}
			}
		}
		for k, v := range h.vars {
			merged[k] = v
		}

		if why, skip := unreachable(merged); skip {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (ansible_connection=%s)", h.name, why))
			continue
		}
		if !validName(h.name) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("line %d: %q cannot be an ssh_config host name", h.line, h.name))
			continue
		}

		r := Record{Name: h.name, Where: fmt.Sprintf("line %d", h.line)}
		for k, v := range merged {
			ansibleVar(&r, k, v)
		}
		r.Groups = membership(groups, order, h.name)
		r.Group = bestPath(paths, r.Groups)
		if len(r.Groups) > 1 && r.Group != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s is in %d groups (%s); tram keeps one, and chose %s",
				h.name, len(r.Groups), strings.Join(r.Groups, ", "), r.Group))
		}
		res.Records = append(res.Records, r)
	}
	return res, nil
}

// splitHostLine splits a host line into its name and its key=value pairs,
// keeping quoted values together.
func splitHostLine(line string) []string {
	var (
		out []string
		cur strings.Builder
		q   byte
	)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case q != 0:
			cur.WriteByte(c)
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t':
			flush()
		case c == '#':
			flush()
			return out
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func splitVar(s string) (string, string, bool) {
	i := strings.Index(s, "=")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
}

// membership lists the groups a host appears in directly, in the order the
// file declared them.
func membership(groups map[string]*iniGroup, order []string, host string) []string {
	var out []string
	for _, name := range order {
		for _, h := range groups[name].hosts {
			if h == host {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// ancestry returns a group's chain from the outermost parent down to itself.
func ancestry(groups map[string]*iniGroup, name string) []string {
	var chain []string
	seen := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		if seen[n] || groups[n] == nil {
			return
		}
		seen[n] = true
		// Only the first parent is followed. A group with two parents has no
		// single path, and inventing one would put hosts somewhere arbitrary.
		if len(groups[n].parents) > 0 {
			walk(groups[n].parents[0])
		}
		chain = append(chain, n)
	}
	walk(name)
	return chain
}

// groupPaths turns the children graph into one slash-separated path per group.
func groupPaths(groups map[string]*iniGroup, order []string, flat bool) map[string]string {
	paths := map[string]string{}
	for _, name := range order {
		if name == "all" || name == "ungrouped" {
			continue
		}
		if flat {
			paths[name] = name
			continue
		}
		chain := ancestry(groups, name)
		var parts []string
		for _, c := range chain {
			if c == "all" || c == "ungrouped" {
				continue
			}
			parts = append(parts, c)
		}
		paths[name] = strings.Join(parts, "/")
	}
	return paths
}

// bestPath picks one group for a host that belongs to several.
//
// The most specific path wins, because a host in both `prod` and `prod/web` is
// more usefully filed under the deeper one. Ties are broken alphabetically so
// that importing the same file twice gives the same answer.
func bestPath(paths map[string]string, groups []string) string {
	var candidates []string
	for _, g := range groups {
		if p := paths[g]; p != "" {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := strings.Count(candidates[i], "/"), strings.Count(candidates[j], "/")
		if di != dj {
			return di > dj
		}
		return candidates[i] < candidates[j]
	})
	return candidates[0]
}

// expandRange turns an Ansible host pattern such as web[01:04] into the names
// it stands for. Zero padding in the first bound is preserved, which is the
// whole point of writing 01 rather than 1.
func expandRange(pattern string) ([]string, error) {
	open := strings.Index(pattern, "[")
	if open < 0 {
		return []string{pattern}, nil
	}
	close := strings.Index(pattern[open:], "]")
	if close < 0 {
		return []string{pattern}, nil
	}
	close += open

	prefix, body, suffix := pattern[:open], pattern[open+1:close], pattern[close+1:]
	parts := strings.Split(body, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return []string{pattern}, nil
	}
	step := 1
	if len(parts) == 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("range %q has a bad step", body)
		}
		step = n
	}

	var names []string
	lo, loErr := strconv.Atoi(parts[0])
	hi, hiErr := strconv.Atoi(parts[1])
	switch {
	case loErr == nil && hiErr == nil:
		if hi < lo {
			return nil, fmt.Errorf("range %q counts backwards", body)
		}
		if (hi-lo)/step > 4096 {
			return nil, fmt.Errorf("range %q would expand to more than 4096 hosts", body)
		}
		width := len(parts[0])
		for n := lo; n <= hi; n += step {
			s := strconv.Itoa(n)
			if len(parts[0]) > 1 && parts[0][0] == '0' {
				for len(s) < width {
					s = "0" + s
				}
			}
			names = append(names, prefix+s+suffix)
		}
	case len(parts[0]) == 1 && len(parts[1]) == 1:
		lo, hi := parts[0][0], parts[1][0]
		if hi < lo {
			return nil, fmt.Errorf("range %q counts backwards", body)
		}
		for c := lo; c <= hi; c += byte(step) {
			names = append(names, prefix+string(c)+suffix)
		}
	default:
		return []string{pattern}, nil
	}

	// A pattern can hold more than one range.
	var out []string
	for _, n := range names {
		more, err := expandRange(n)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}
