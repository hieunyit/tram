// Package model holds the plain types tram passes around. It performs no I/O
// and reads no files, so every other package can depend on it freely.
package model

import (
	"sort"
	"strconv"
	"strings"
)

// Host is one machine as tram sees it: the parts of an ssh_config stanza tram
// understands, plus the few facts that live outside the file.
type Host struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`

	HostName      string   `json:"hostname"`
	User          string   `json:"user"`
	Port          string   `json:"port"`
	IdentityFiles []string `json:"identity_files"`
	ProxyJump     string   `json:"proxy_jump"`

	Group string `json:"group"`
	Desc  string `json:"desc"`

	// Tags are free labels kept in a comment beside the stanza. A group says
	// where a host sits in the hierarchy and a host has exactly one; tags say
	// what it is, and a host can have as many as it needs.
	Tags []string `json:"tags,omitempty"`

	// Account is the identity this host is linked to, and Drift is set when the
	// host's own User or IdentityFile no longer match what that account would
	// write. A drifted host is never overwritten by apply.
	Account string `json:"account"`
	Drift   bool   `json:"drift"`

	// File is the configuration file that declares the stanza, which is where
	// an edit has to be written back.
	File     string `json:"file"`
	Line     int    `json:"line"`
	ReadOnly bool   `json:"read_only"`

	// Other holds directives tram does not model, kept so that ls can show them
	// and so that nothing is lost when a host is cloned.
	Other map[string][]string `json:"other,omitempty"`

	// LastUsed is a Unix timestamp from tram's own history, zero when unknown.
	LastUsed int64 `json:"last_used"`
	Favorite bool  `json:"favorite"`
}

// Addr returns the address ssh will dial, falling back to the host's name when
// no HostName is set, which is what ssh itself does.
func (h Host) Addr() string {
	if h.HostName != "" {
		return h.HostName
	}
	return h.Name
}

// PortOr returns the port, or "22" when the stanza does not set one.
func (h Host) PortOr() string {
	if h.Port == "" {
		return "22"
	}
	return h.Port
}

// Target renders the user@host:port form used in listings.
func (h Host) Target() string {
	s := h.Addr()
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port != "" && h.Port != "22" {
		s += ":" + h.Port
	}
	return s
}

// GroupPath splits a hierarchical group such as "prod/web" into its segments.
func (h Host) GroupPath() []string {
	if h.Group == "" {
		return nil
	}
	return strings.Split(h.Group, "/")
}

// InGroup reports whether the host belongs to g or to any group nested under
// it, so filtering on "prod" also returns the hosts in "prod/web".
func (h Host) InGroup(g string) bool {
	if g == "" {
		return true
	}
	g = strings.Trim(g, "/")
	return strings.EqualFold(h.Group, g) ||
		strings.HasPrefix(strings.ToLower(h.Group), strings.ToLower(g)+"/")
}

// Matches reports whether any of the host's searchable fields contains q,
// compared case-insensitively. A query starting with '#' matches groups only.
func (h Host) Matches(q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	if strings.HasPrefix(q, "#") {
		want := strings.TrimPrefix(q, "#")
		if strings.Contains(strings.ToLower(h.Group), want) {
			return true
		}
		// A # query asks about labels rather than names, and a tag is a label,
		// so it answers for both rather than making the user remember which
		// prefix belongs to which.
		return h.HasTag(want)
	}
	fields := []string{h.Name, h.HostName, h.User, h.Group, h.Desc, h.Account, h.ProxyJump}
	fields = append(fields, h.Aliases...)
	fields = append(fields, h.Tags...)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// HasTag reports whether any tag contains q, compared case-insensitively.
func (h Host) HasTag(q string) bool {
	for _, t := range h.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// TagList renders the tags the way they are written in the file.
func (h Host) TagList() string { return strings.Join(h.Tags, ", ") }

// ParseTags splits a typed list into tags, dropping blanks and duplicates so
// that "web,, web , api" is two tags rather than four.
func ParseTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		t := strings.TrimSpace(part)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// PortNum returns the port as a number, defaulting to 22.
func (h Host) PortNum() int {
	if n, err := strconv.Atoi(h.Port); err == nil && n > 0 {
		return n
	}
	return 22
}

// SortHosts orders hosts by group and then by name, which is the order every
// listing uses so that output is stable between runs.
func SortHosts(hs []Host) {
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Group != hs[j].Group {
			return hs[i].Group < hs[j].Group
		}
		return strings.ToLower(hs[i].Name) < strings.ToLower(hs[j].Name)
	})
}

// Names extracts the names of a slice of hosts.
func Names(hs []Host) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Name
	}
	return out
}
