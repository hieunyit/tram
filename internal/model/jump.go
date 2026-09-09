package model

import (
	"strings"
)

// Hop is one station on a ProxyJump route.
type Hop struct {
	// Spec is the token exactly as written in the ProxyJump directive.
	Spec string `json:"spec"`
	// Host is the name part of the token, which may or may not name a stanza.
	Host string `json:"host"`
	User string `json:"user,omitempty"`
	Port string `json:"port,omitempty"`
	// Known is true when Host names a stanza in the configuration, so tram can
	// say something about it beyond its name.
	Known bool `json:"known"`
	// Addr is the address the stanza gives, when there is one.
	Addr string `json:"addr,omitempty"`
}

// JumpChain is the full route to a host: every station, in the order ssh walks
// them, with the destination itself excluded.
type JumpChain struct {
	Hops []Hop `json:"hops"`
	// Cycle names the hosts involved in a ProxyJump loop, in the order found.
	// A loop makes ssh hang rather than fail, so tram refuses to open such a
	// route and says which stations form it.
	Cycle []string `json:"cycle,omitempty"`
	// Unknown lists hops that name no stanza and no resolvable address.
	Unknown []string `json:"unknown,omitempty"`
}

// Broken reports whether the route cannot be used as it stands.
func (c JumpChain) Broken() bool { return len(c.Cycle) > 0 }

// Empty reports whether the host is reached directly.
func (c JumpChain) Empty() bool { return len(c.Hops) == 0 }

// String renders the route the way ls shows it.
func (c JumpChain) String() string {
	if len(c.Cycle) > 0 {
		return "loop: " + strings.Join(c.Cycle, " -> ")
	}
	if len(c.Hops) == 0 {
		return ""
	}
	parts := make([]string, len(c.Hops))
	for i, h := range c.Hops {
		parts[i] = h.Spec
	}
	return strings.Join(parts, " -> ")
}

// ParseJumpSpec splits one ProxyJump token into its user, host and port parts.
// The forms ssh accepts are host, user@host, host:port and user@host:port.
func ParseJumpSpec(s string) Hop {
	h := Hop{Spec: s, Host: s}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		h.User, h.Host = s[:i], s[i+1:]
	}
	// A bare IPv6 address is bracketed; only split on a colon outside brackets.
	if !strings.HasPrefix(h.Host, "[") {
		if i := strings.LastIndex(h.Host, ":"); i >= 0 && !strings.Contains(h.Host[i+1:], ":") {
			h.Port = h.Host[i+1:]
			h.Host = h.Host[:i]
		}
	} else if i := strings.LastIndex(h.Host, "]:"); i >= 0 {
		h.Port = h.Host[i+2:]
		h.Host = strings.Trim(h.Host[:i+1], "[]")
	}
	return h
}

// SplitJump splits a ProxyJump value into its comma-separated tokens.
func SplitJump(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" && !strings.EqualFold(p, "none") {
			out = append(out, p)
		}
	}
	return out
}

// ResolveChain walks the ProxyJump route to name, expanding each station that
// is itself a configured host with its own ProxyJump.
//
// A loop is detected rather than followed. This matters more than it sounds:
// ssh given a circular ProxyJump does not report an error, it hangs, so a tool
// that does not check for loops hands the user a session that never opens.
func ResolveChain(name string, lookup func(string) (Host, bool)) JumpChain {
	var chain JumpChain
	onPath := []string{name}
	seen := map[string]bool{strings.ToLower(name): true}

	cur, ok := lookup(name)
	if !ok {
		return chain
	}

	for {
		specs := SplitJump(cur.ProxyJump)
		if len(specs) == 0 {
			return chain
		}
		// Every token but the last is a station tram cannot expand further in
		// this pass; ssh dials them in order, so record them as written.
		for i, s := range specs {
			hop := ParseJumpSpec(s)
			if h, ok := lookup(hop.Host); ok {
				hop.Known = true
				hop.Addr = h.Addr()
			}
			if i < len(specs)-1 {
				chain.Hops = append(chain.Hops, hop)
				if !hop.Known {
					chain.Unknown = append(chain.Unknown, hop.Host)
				}
				continue
			}

			// The last token is the station nearest the destination; follow its
			// own ProxyJump so that a route of routes resolves fully.
			key := strings.ToLower(hop.Host)
			if seen[key] {
				chain.Cycle = append(append([]string(nil), onPath...), hop.Host)
				return chain
			}
			next, known := lookup(hop.Host)
			hop.Known = known
			if !known {
				chain.Hops = append([]Hop{hop}, chain.Hops...)
				chain.Unknown = append(chain.Unknown, hop.Host)
				return chain
			}
			hop.Addr = next.Addr()
			chain.Hops = append([]Hop{hop}, chain.Hops...)
			seen[key] = true
			onPath = append(onPath, hop.Host)
			cur = next
		}
	}
}

// Dependents returns the hosts whose ProxyJump names target, that is the hosts
// that break when the station is removed.
func Dependents(target string, hosts []Host) []Host {
	var out []Host
	for _, h := range hosts {
		for _, s := range SplitJump(h.ProxyJump) {
			if strings.EqualFold(ParseJumpSpec(s).Host, target) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// RenameJump rewrites a ProxyJump value so that every reference to oldName
// becomes newName, keeping the user and port parts of each token intact.
func RenameJump(value, oldName, newName string) string {
	specs := SplitJump(value)
	if len(specs) == 0 {
		return value
	}
	changed := false
	for i, s := range specs {
		hop := ParseJumpSpec(s)
		if !strings.EqualFold(hop.Host, oldName) {
			continue
		}
		changed = true
		out := newName
		if hop.User != "" {
			out = hop.User + "@" + out
		}
		if hop.Port != "" {
			out += ":" + hop.Port
		}
		specs[i] = out
	}
	if !changed {
		return value
	}
	return strings.Join(specs, ",")
}

// WouldCycle reports whether pointing from's ProxyJump at to would create a
// loop, which is what the jump picker uses to grey out impossible choices.
func WouldCycle(from, to string, lookup func(string) (Host, bool)) bool {
	if strings.EqualFold(from, to) {
		return true
	}
	seen := map[string]bool{strings.ToLower(from): true}
	cur := to
	for i := 0; i < 64; i++ {
		if seen[strings.ToLower(cur)] {
			return true
		}
		seen[strings.ToLower(cur)] = true
		h, ok := lookup(cur)
		if !ok {
			return false
		}
		specs := SplitJump(h.ProxyJump)
		if len(specs) == 0 {
			return false
		}
		cur = ParseJumpSpec(specs[len(specs)-1]).Host
	}
	return true
}
