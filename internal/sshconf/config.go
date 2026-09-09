package sshconf

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is a root configuration file together with everything it includes.
type Config struct {
	Root    *File
	Files   []*File // load order, root first
	Home    string
	BaseDir string // directory relative Include paths resolve against

	byPath map[string]*File
	stream []streamEntry // flattened line stream in ssh's evaluation order
	errs   []error
}

// streamEntry is one line of the flattened configuration, tagged with the file
// it came from so that an edit can be written back to the right place.
type streamEntry struct {
	file *File
	idx  int
}

// LoadOptions controls how a configuration tree is read.
type LoadOptions struct {
	Home    string // defaults to the user's home directory
	BaseDir string // defaults to <Home>/.ssh
}

// Load reads path and every file it includes.
func Load(path string, opt LoadOptions) (*Config, error) {
	if opt.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home directory: %w", err)
		}
		opt.Home = h
	}
	if opt.BaseDir == "" {
		opt.BaseDir = filepath.Join(opt.Home, ".ssh")
	}

	c := &Config{Home: opt.Home, BaseDir: opt.BaseDir, byPath: map[string]*File{}}

	root, err := ParseFile(path)
	if os.IsNotExist(err) {
		root = Parse(nil, path)
	} else if err != nil {
		return nil, err
	}
	c.Root = root
	c.add(root)
	c.stream = c.flatten(root, 0, map[string]bool{key(path): true})
	return c, nil
}

// LoadFrom builds a configuration from an already parsed root file, used by
// tests that keep everything in memory.
func LoadFrom(root *File, opt LoadOptions) *Config {
	c := &Config{Root: root, Home: opt.Home, BaseDir: opt.BaseDir, byPath: map[string]*File{}}
	c.add(root)
	c.stream = c.flatten(root, 0, map[string]bool{key(root.Path): true})
	return c
}

func key(p string) string { return strings.ToLower(filepath.Clean(p)) }

func (c *Config) add(f *File) {
	if _, seen := c.byPath[key(f.Path)]; seen {
		return
	}
	c.byPath[key(f.Path)] = f
	c.Files = append(c.Files, f)
}

// flatten walks a file's lines and splices included files in at the point of
// their Include directive, which is exactly where ssh reads them.
func (c *Config) flatten(f *File, depth int, onPath map[string]bool) []streamEntry {
	if depth > maxIncludeDepth {
		c.errs = append(c.errs, fmt.Errorf("%s: Include nested more than %d deep", f.Path, maxIncludeDepth))
		return nil
	}
	var out []streamEntry
	for i, l := range f.Lines {
		if !l.Is("Include") {
			out = append(out, streamEntry{f, i})
			continue
		}
		out = append(out, streamEntry{f, i})
		for _, arg := range l.Args {
			for _, p := range globInclude(arg, c.BaseDir, c.Home) {
				if onPath[key(p)] {
					c.errs = append(c.errs, fmt.Errorf("%s: Include loop through %s", f.Path, p))
					continue
				}
				sub, ok := c.byPath[key(p)]
				if !ok {
					var err error
					sub, err = ParseFile(p)
					if err != nil {
						c.errs = append(c.errs, fmt.Errorf("include %s: %w", p, err))
						continue
					}
					c.add(sub)
				}
				onPath[key(p)] = true
				out = append(out, c.flatten(sub, depth+1, onPath)...)
				delete(onPath, key(p))
			}
		}
	}
	return out
}

// Errors returns problems found while loading, such as a missing include or an
// include loop. They are reported rather than fatal so that a partly broken
// configuration can still be inspected.
func (c *Config) Errors() []error { return c.errs }

// File returns the loaded file at path, or nil.
func (c *Config) File(path string) *File { return c.byPath[key(path)] }

// Blocks returns every stanza across every file, in evaluation order.
func (c *Config) Blocks() []*Block {
	var out []*Block
	seen := map[*Block]bool{}
	for _, e := range c.stream {
		for _, b := range e.file.Blocks {
			if b.Head == e.idx && b.Type != BlockGlobal && !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

// HostBlocks returns the Host stanzas in evaluation order.
func (c *Config) HostBlocks() []*Block {
	var out []*Block
	for _, b := range c.Blocks() {
		if b.Type == BlockHost {
			out = append(out, b)
		}
	}
	return out
}

// FindHost returns the first stanza that names host literally, that is a Host
// line listing the name without wildcards. Wildcard stanzas are never returned
// because they describe a class rather than a machine.
func (c *Config) FindHost(name string) *Block {
	for _, b := range c.HostBlocks() {
		for _, n := range b.Names() {
			if foldEqual(n, name) {
				return b
			}
		}
	}
	return nil
}

// Duplicates reports names declared by more than one stanza, which is legal but
// means the later stanzas are dead for most keywords.
func (c *Config) Duplicates() map[string][]*Block {
	byName := map[string][]*Block{}
	for _, b := range c.HostBlocks() {
		for _, n := range b.Names() {
			byName[strings.ToLower(n)] = append(byName[strings.ToLower(n)], b)
		}
	}
	for n, bs := range byName {
		if len(bs) < 2 {
			delete(byName, n)
		}
	}
	return byName
}

// Effective computes the settings ssh would use for host, applying stanzas in
// order and keeping the first value seen for each keyword, which is ssh's rule.
// Repeated keywords such as IdentityFile accumulate instead.
func (c *Config) Effective(host string) map[string][]string {
	out := map[string][]string{}
	active := true // the file preamble applies to everything

	for _, e := range c.stream {
		l := e.file.Lines[e.idx]
		if l.Kind != LineDirective {
			continue
		}
		switch {
		case l.Is("Host"):
			active = MatchAny(l.Args, host)
			continue
		case l.Is("Match"):
			// Match conditions can depend on runtime state tram does not have,
			// so its stanzas are never treated as active. Reading is safe;
			// pretending to evaluate them would not be.
			active = false
			continue
		case l.Is("Include"):
			continue
		}
		if !active {
			continue
		}
		kw := strings.ToLower(l.Keyword)
		if isMultiValue(kw) {
			out[kw] = append(out[kw], l.Args...)
			continue
		}
		if _, seen := out[kw]; !seen {
			out[kw] = append([]string(nil), l.Args...)
		}
	}
	return out
}

// multiValue lists the keywords where ssh accumulates every occurrence rather
// than keeping only the first.
var multiValue = map[string]bool{
	"identityfile":     true,
	"certificatefile":  true,
	"localforward":     true,
	"remoteforward":    true,
	"dynamicforward":   true,
	"sendenv":          true,
	"setenv":           true,
	"permitremoteopen": true,
	"canonicaldomains": true,
}

func isMultiValue(kwLower string) bool { return multiValue[kwLower] }

// Refresh recomputes the flattened line stream.
//
// Editing a file changes its line numbering, and the stream is a list of line
// positions, so it goes stale the moment a stanza is added or removed. Every
// mutation must be followed by this, or a newly written host is invisible to
// the very lookup that was meant to find it.
func (c *Config) Refresh() {
	c.errs = nil
	c.stream = c.flatten(c.Root, 0, map[string]bool{key(c.Root.Path): true})
}
