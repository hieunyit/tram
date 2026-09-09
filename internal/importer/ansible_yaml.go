package importer

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseYAML reads the YAML form of an Ansible inventory.
//
// The shape is a tree: a group has `hosts`, `vars` and `children`, and children
// are groups again. tram walks it once, carrying the variables down, and turns
// the nesting into its own group path.
func parseYAML(data []byte, opt Options) (*Result, error) {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if root == nil {
		return &Result{}, nil
	}

	res := &Result{}
	// A host can appear under several groups; the deepest path wins, and the
	// variables from every appearance are merged.
	found := map[string]*yamlHost{}
	var order []string

	var walk func(name string, node any, path []string, inherited map[string]string)
	walk = func(name string, node any, path []string, inherited map[string]string) {
		group, ok := node.(map[string]any)
		if !ok {
			return
		}

		vars := copyVars(inherited)
		for k, v := range mapOf(group["vars"]) {
			vars[k] = scalar(v)
		}

		here := path
		if name != "all" && name != "ungrouped" && name != "" {
			here = append(append([]string(nil), path...), name)
		}
		groupPath := strings.Join(here, "/")
		if opt.FlatGroups && name != "" && name != "all" && name != "ungrouped" {
			groupPath = name
		}

		for hostName, hostNode := range mapOf(group["hosts"]) {
			names, err := expandRange(hostName)
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			hostVars := copyVars(vars)
			for k, v := range mapOf(hostNode) {
				hostVars[k] = scalar(v)
			}
			for _, n := range names {
				h, seen := found[n]
				if !seen {
					h = &yamlHost{name: n, vars: map[string]string{}}
					found[n] = h
					order = append(order, n)
				}
				for k, v := range hostVars {
					h.vars[k] = v
				}
				h.groups = append(h.groups, orUngrouped(name))
				if strings.Count(groupPath, "/") >= strings.Count(h.path, "/") && groupPath != "" {
					h.path = groupPath
				}
			}
		}

		children := mapOf(group["children"])
		names := make([]string, 0, len(children))
		for childName := range children {
			names = append(names, childName)
		}
		// Sorted so that repeated imports of one file agree with each other.
		sort.Strings(names)
		for _, childName := range names {
			walk(childName, children[childName], here, vars)
		}
	}

	// Both layouts appear: everything under `all`, or the groups at the top.
	if all, ok := root["all"]; ok {
		walk("all", all, nil, map[string]string{})
	} else {
		names := make([]string, 0, len(root))
		for k := range root {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			walk(k, root[k], nil, map[string]string{})
		}
	}

	for _, name := range order {
		h := found[name]
		if why, skip := unreachable(h.vars); skip {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (ansible_connection=%s)", name, why))
			continue
		}
		if !validName(name) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%q cannot be an ssh_config host name", name))
			continue
		}
		r := Record{Name: name, Group: h.path, Groups: h.groups, Where: "group " + strings.Join(h.groups, ", ")}
		for k, v := range h.vars {
			ansibleVar(&r, k, v)
		}
		res.Records = append(res.Records, r)
	}
	return res, nil
}

type yamlHost struct {
	name   string
	vars   map[string]string
	groups []string
	path   string
}

func orUngrouped(name string) string {
	if name == "" {
		return "ungrouped"
	}
	return name
}

func copyVars(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// mapOf reads a YAML node as a mapping, tolerating the null value a host with
// no variables of its own produces.
func mapOf(node any) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		return v
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[fmt.Sprint(k)] = val
		}
		return out
	case []any:
		// A `hosts:` written as a list of names rather than a mapping.
		out := make(map[string]any, len(v))
		for _, item := range v {
			out[fmt.Sprint(item)] = nil
		}
		return out
	}
	return nil
}

// scalar renders a YAML value as the string ssh_config would hold. A port
// written as a number has to come out as "2222", not "2222.000000".
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	}
	return fmt.Sprint(v)
}
