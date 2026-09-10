package model

import (
	"sort"
	"strings"
)

// GroupNode is one level of the group hierarchy.
type GroupNode struct {
	// Name is the label at this level, "web" in "prod/web".
	Name string `json:"name"`
	// Path is the whole path from the root, "prod/web".
	Path string `json:"path"`
	// Depth is 0 for a top-level group.
	Depth int `json:"depth"`
	// Direct counts the hosts filed exactly here.
	Direct int `json:"direct"`
	// Total counts those and everything nested below.
	Total    int          `json:"total"`
	Children []*GroupNode `json:"children,omitempty"`
}

// NormaliseGroup tidies a group path: whitespace around each level is dropped
// and empty levels disappear, so "prod / web /" and "prod/web" are one group
// rather than two that merely look alike.
func NormaliseGroup(g string) string {
	parts := strings.Split(g, "/")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

// BuildGroupTree turns the group paths in use into a hierarchy.
//
// A level nobody filed a host in directly is still created when something sits
// below it: a configuration with only "prod/web" and "prod/db" still gets a
// "prod" to nest them under, because otherwise the tree would have no trunk.
func BuildGroupTree(hosts []Host) []*GroupNode {
	byPath := map[string]*GroupNode{}
	var roots []*GroupNode

	ensure := func(path string) *GroupNode {
		if n, ok := byPath[path]; ok {
			return n
		}
		parts := strings.Split(path, "/")
		n := &GroupNode{Name: parts[len(parts)-1], Path: path, Depth: len(parts) - 1}
		byPath[path] = n
		if len(parts) == 1 {
			roots = append(roots, n)
		}
		return n
	}

	for _, h := range hosts {
		g := NormaliseGroup(h.Group)
		if g == "" {
			continue
		}
		parts := strings.Split(g, "/")
		for i := range parts {
			path := strings.Join(parts[:i+1], "/")
			n := ensure(path)
			n.Total++
			if i > 0 {
				parent := byPath[strings.Join(parts[:i], "/")]
				if !hasChild(parent, n) {
					parent.Children = append(parent.Children, n)
				}
			}
		}
		byPath[g].Direct++
	}

	sortNodes(roots)
	return roots
}

func hasChild(parent, child *GroupNode) bool {
	for _, c := range parent.Children {
		if c == child {
			return true
		}
	}
	return false
}

func sortNodes(nodes []*GroupNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	for _, n := range nodes {
		sortNodes(n.Children)
	}
}

// Ungrouped counts the hosts with no group at all, which need somewhere to be
// listed or they would be invisible behind a group tree.
func Ungrouped(hosts []Host) int {
	n := 0
	for _, h := range hosts {
		if NormaliseGroup(h.Group) == "" {
			n++
		}
	}
	return n
}
