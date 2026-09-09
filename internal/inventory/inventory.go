// Package inventory joins the two halves of tram: the ssh_config tree read by
// sshconf, and tram's own state read by store. It produces the host list every
// command works from, and it owns every write back to disk.
package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/sshconf"
	"github.com/hieuny/tram/internal/store"
)

// Metadata markers tram writes into a stanza for the two facts ssh has no
// directive for. They are ordinary comments, so a config carrying them is still
// a plain ssh_config and ssh ignores them.
const (
	groupMarker = "#tram-group:"
	descMarker  = "#tram-desc:"
)

// Inventory is the loaded configuration tree plus tram's own state.
type Inventory struct {
	Config *sshconf.Config
	Store  *store.Store

	// Managed is tram's own file when it exists. Its presence turns on the
	// read-only lock: once you have run init, tram will not write to a stanza
	// you keep somewhere else unless you ask it to.
	Managed *sshconf.File

	hosts  []model.Host
	byName map[string]*sshconf.Block
}

// Options controls where the inventory reads from.
type Options struct {
	ConfigPath string
	SSHDir     string
	Home       string
}

// Load reads ssh_config and everything it includes, then decorates the result
// with account links, history and favorites.
func Load(opt Options) (*Inventory, error) {
	if opt.ConfigPath == "" {
		opt.ConfigPath = store.ConfigPath()
	}
	if opt.SSHDir == "" {
		opt.SSHDir = store.SSHDir()
	}
	if opt.Home == "" {
		opt.Home, _ = os.UserHomeDir()
	}

	cfg, err := sshconf.Load(opt.ConfigPath, sshconf.LoadOptions{Home: opt.Home, BaseDir: opt.SSHDir})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", opt.ConfigPath, err)
	}

	inv := &Inventory{Config: cfg, Store: store.Load()}
	managedPath := filepath.Join(opt.SSHDir, "config.d", store.ManagedName)
	if f := cfg.File(managedPath); f != nil {
		f.Managed = true
		inv.Managed = f
	}
	inv.rebuild()
	return inv, nil
}

// Errors reports problems found while reading the configuration tree.
func (inv *Inventory) Errors() []error { return inv.Config.Errors() }

// rebuild recomputes the host list from the current parse tree. Every write
// calls it so the in-memory view never lags the file.
func (inv *Inventory) rebuild() {
	inv.Config.Refresh()
	inv.hosts = nil
	inv.byName = map[string]*sshconf.Block{}

	for _, b := range inv.Config.HostBlocks() {
		names := b.Names()
		if len(names) == 0 {
			continue // a wildcard-only stanza names no machine
		}
		h := hostFromBlock(b, names)
		h.ReadOnly = inv.isReadOnly(b.File)
		h.Account = inv.Store.LinkOf(h.Name)
		h.LastUsed = inv.Store.LastUsed(h.Name)
		h.Favorite = inv.Store.IsFavorite(h.Name)
		if h.Account != "" {
			if a, ok := inv.Store.Account(h.Account); ok {
				h.Drift = driftFrom(a, h)
			}
		}
		if _, dup := inv.byName[strings.ToLower(h.Name)]; !dup {
			inv.byName[strings.ToLower(h.Name)] = b
		}
		inv.hosts = append(inv.hosts, h)
	}
	model.SortHosts(inv.hosts)
}

// isReadOnly reports whether a file is off limits without --force. Before init
// there is no managed file and the whole tree is writable; after init only
// tram's own file is.
func (inv *Inventory) isReadOnly(f *sshconf.File) bool {
	if inv.Managed == nil {
		return false
	}
	return f != inv.Managed
}

// hostFromBlock reads the directives tram models out of a stanza and keeps the
// rest in Other so nothing is invisible.
func hostFromBlock(b *sshconf.Block, names []string) model.Host {
	h := model.Host{
		Name:          names[0],
		Aliases:       names[1:],
		HostName:      b.GetOne("HostName"),
		User:          b.GetOne("User"),
		Port:          b.GetOne("Port"),
		IdentityFiles: b.GetAll("IdentityFile"),
		ProxyJump:     b.GetOne("ProxyJump"),
		File:          b.File.Path,
		Line:          b.Head + 1,
	}
	known := map[string]bool{"hostname": true, "user": true, "port": true, "identityfile": true, "proxyjump": true}
	for _, l := range b.Body() {
		switch {
		case l.Kind == sshconf.LineComment:
			if v, ok := markerValue(l.Raw, groupMarker); ok {
				h.Group = v
			}
			if v, ok := markerValue(l.Raw, descMarker); ok {
				h.Desc = v
			}
		case l.Kind == sshconf.LineDirective && !known[strings.ToLower(l.Keyword)]:
			k := l.Keyword
			if h.Other == nil {
				h.Other = map[string][]string{}
			}
			h.Other[k] = append(h.Other[k], strings.Join(l.Args, " "))
		}
	}
	return h
}

func markerValue(raw, marker string) (string, bool) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(t, marker) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(t, marker)), true
}

// driftFrom reports whether a host no longer matches the identity it is linked
// to, which happens when the stanza is edited by hand. A drifted host keeps its
// link but is never overwritten by apply.
func driftFrom(a model.Account, h model.Host) bool {
	user, keys := a.Materialized()
	if h.User != user {
		return true
	}
	if len(keys) == 0 {
		return false
	}
	for _, k := range h.IdentityFiles {
		if samePath(k, keys[0]) {
			return false
		}
	}
	return true
}

func samePath(a, b string) bool {
	norm := func(s string) string {
		s = strings.Trim(s, `"`)
		s = strings.ReplaceAll(s, `\`, "/")
		return strings.ToLower(strings.TrimSuffix(s, "/"))
	}
	return norm(a) == norm(b)
}

// ---- reading --------------------------------------------------------------

// Hosts returns every host in the tree, sorted by group then name.
func (inv *Inventory) Hosts() []model.Host { return append([]model.Host(nil), inv.hosts...) }

// Host returns the host with an exact name match.
func (inv *Inventory) Host(name string) (model.Host, bool) {
	for _, h := range inv.hosts {
		if strings.EqualFold(h.Name, name) {
			return h, true
		}
		for _, a := range h.Aliases {
			if strings.EqualFold(a, name) {
				return h, true
			}
		}
	}
	return model.Host{}, false
}

// Lookup is the callback the ProxyJump resolver needs.
func (inv *Inventory) Lookup(name string) (model.Host, bool) { return inv.Host(name) }

// Chain resolves the ProxyJump route to a host.
func (inv *Inventory) Chain(name string) model.JumpChain {
	return model.ResolveChain(name, inv.Lookup)
}

// Resolve turns a name typed on the command line into hosts.
//
// An exact name wins outright. Only when nothing matches exactly does tram fall
// back to substring matching, and several fuzzy matches are returned rather
// than guessed between, because `db-prod` and `db-prod-replica` are not the
// same machine and picking one silently is how you restart the wrong database.
func (inv *Inventory) Resolve(q string) []model.Host {
	var exact, fuzzy []model.Host
	lq := strings.ToLower(q)
	for _, h := range inv.hosts {
		names := append([]string{h.Name}, h.Aliases...)
		hit := false
		for _, n := range names {
			if strings.EqualFold(n, q) {
				exact = append(exact, h)
				hit = true
				break
			}
		}
		if hit {
			continue
		}
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), lq) {
				fuzzy = append(fuzzy, h)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fuzzy
}

// Filter selects hosts by group and free-text search, which is what every
// listing and fleet command narrows with.
type Filter struct {
	Group  string
	Search string
	All    bool
	Names  []string
}

// Select applies a filter to the host list.
func (inv *Inventory) Select(f Filter) []model.Host {
	var out []model.Host
	for _, h := range inv.hosts {
		if f.Group != "" && !h.InGroup(f.Group) {
			continue
		}
		if f.Search != "" && !h.Matches(f.Search) {
			continue
		}
		if len(f.Names) > 0 {
			hit := false
			for _, n := range f.Names {
				if strings.EqualFold(n, h.Name) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, h)
	}
	return out
}

// Groups returns every group in use, sorted, including parent groups implied by
// a nested one.
func (inv *Inventory) Groups() []string {
	seen := map[string]bool{}
	for _, h := range inv.hosts {
		parts := h.GroupPath()
		for i := range parts {
			seen[strings.Join(parts[:i+1], "/")] = true
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Block returns the stanza that declares a host, for callers that need to edit
// it directly.
func (inv *Inventory) Block(name string) *sshconf.Block {
	return inv.byName[strings.ToLower(name)]
}
