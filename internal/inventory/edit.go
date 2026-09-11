package inventory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hieuny/tram/internal/importer"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/sshconf"
	"github.com/hieuny/tram/internal/store"
)

// ErrReadOnly is returned when a stanza lives outside tram's managed file and
// --force was not given.
var ErrReadOnly = errors.New("host is declared outside tram's managed file")

// Spec describes a change to a host. A nil pointer field means leave the
// current value alone; a pointer to the empty string means remove it. That
// distinction is what lets `tram edit web1 --account ""` unlink an account
// without also wiping the User line the account had written.
type Spec struct {
	Name    string
	Aliases []string

	HostName  *string
	User      *string
	Port      *string
	ProxyJump *string
	Group     *string
	Desc      *string
	// Tags is the whole list as typed, split on commas and spaces when written.
	Tags *string

	// IdentityFiles replaces every IdentityFile line when non-nil. A non-nil
	// empty slice removes them all.
	IdentityFiles []string
	setIdentity   bool

	// Account links the host to an identity. Setting it also materializes the
	// account's User and IdentityFile into the stanza.
	Account *string

	// Extra carries directives tram does not model, used by import and clone.
	Extra []sshconf.Directive
}

// SetIdentityFiles records an explicit IdentityFile list, including an empty
// one, which a nil slice alone could not express.
func (s *Spec) SetIdentityFiles(files []string) {
	s.IdentityFiles = files
	s.setIdentity = true
}

// Str is a helper for building a Spec from command-line flags.
func Str(v string) *string { return &v }

// Change is a pending edit: the files it touched, the warnings it raised, and
// the diff a user is shown before anything reaches disk.
type Change struct {
	inv      *Inventory
	Files    []*sshconf.File
	Warnings []string
	Summary  []string
}

// warn records a warning, dropping one that has already been said. Importing
// fifty hosts that all jump through the same unknown station is one problem,
// not fifty.
func (c *Change) warn(msgs ...string) {
	for _, m := range msgs {
		dup := false
		for _, e := range c.Warnings {
			if e == m {
				dup = true
				break
			}
		}
		if !dup {
			c.Warnings = append(c.Warnings, m)
		}
	}
}

func (c *Change) touch(f *sshconf.File) {
	for _, e := range c.Files {
		if e == f {
			return
		}
	}
	c.Files = append(c.Files, f)
}

// Empty reports whether the change would alter nothing on disk.
func (c *Change) Empty() bool { return c.Diff() == "" }

// Diff renders every affected file as a unified diff against what is on disk.
func (c *Change) Diff() string {
	var b strings.Builder
	for _, f := range c.Files {
		b.WriteString(f.Diff())
	}
	return b.String()
}

// Apply writes the change out. Each file is written atomically with a backup
// beside it, so an interrupted run leaves the original in place.
func (c *Change) Apply() error {
	for _, f := range c.Files {
		if err := f.Save(sshconf.SaveOptions{Backup: true}); err != nil {
			return err
		}
	}
	if c.inv != nil {
		c.inv.rebuild()
	}
	return nil
}

// Discard throws the pending edit away by re-reading the files from disk, so a
// dry run leaves the in-memory tree usable for the next command.
func (c *Change) Discard() {
	for _, f := range c.Files {
		if fresh, err := sshconf.ParseFile(f.Path); err == nil {
			*f = *fresh
		}
	}
	if c.inv != nil {
		c.inv.rebuild()
	}
}

// ---- writing --------------------------------------------------------------

// target picks the file a new host goes into: tram's own file once init has
// created it, otherwise the root ssh_config.
func (inv *Inventory) target() *sshconf.File {
	if inv.Managed != nil {
		return inv.Managed
	}
	return inv.Config.Root
}

// directivesFor turns a spec into the directive list a stanza should carry,
// starting from the values already present so that unspecified fields survive.
func (inv *Inventory) directivesFor(cur model.Host, s Spec) (set []sshconf.Directive, unset []string, warn []string) {
	add := func(kw, v string) {
		if v == "" {
			unset = append(unset, kw)
			return
		}
		set = append(set, sshconf.D(kw, v))
	}

	if s.HostName != nil {
		add("HostName", *s.HostName)
	}
	if s.User != nil {
		add("User", *s.User)
	}
	if s.Port != nil {
		p := *s.Port
		if p != "" {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				warn = append(warn, fmt.Sprintf("port %q is not a number between 1 and 65535", p))
			}
		}
		if p == "22" {
			p = "" // ssh's default; writing it adds noise without adding meaning
		}
		add("Port", p)
	}
	if s.ProxyJump != nil {
		add("ProxyJump", *s.ProxyJump)
	}
	if s.setIdentity {
		if len(s.IdentityFiles) == 0 {
			unset = append(unset, "IdentityFile")
		} else {
			for _, f := range s.IdentityFiles {
				set = append(set, sshconf.D("IdentityFile", f))
			}
		}
	}

	// A link to an account materializes that account's identity into the
	// stanza. The account never replaces the stanza's fields at read time; it
	// writes them once, here, so ssh alone still knows how to connect.
	if s.Account != nil && *s.Account != "" {
		a, ok := inv.Store.Account(*s.Account)
		if !ok {
			warn = append(warn, fmt.Sprintf("account %q does not exist", *s.Account))
		} else {
			user, keys := a.Materialized()
			if user != "" && s.User == nil {
				set = append(set, sshconf.D("User", user))
			}
			if len(keys) > 0 && !s.setIdentity {
				for _, k := range keys {
					set = append(set, sshconf.D("IdentityFile", k))
				}
			}
		}
	}

	set = append(set, s.Extra...)
	return set, unset, warn
}

// Add creates a new host stanza.
func (inv *Inventory) Add(s Spec) (*Change, error) {
	if s.Name == "" {
		return nil, errors.New("a host needs a name")
	}
	if err := validName(s.Name); err != nil {
		return nil, err
	}
	if _, exists := inv.Host(s.Name); exists {
		return nil, fmt.Errorf("host %q already exists", s.Name)
	}

	ch := &Change{inv: inv}
	f := inv.target()

	set, _, warn := inv.directivesFor(model.Host{}, s)
	ch.Warnings = append(ch.Warnings, warn...)

	patterns := append([]string{s.Name}, s.Aliases...)
	b, err := sshconf.AddHost(f, patterns, set, "")
	if err != nil {
		return nil, err
	}
	applyMarkers(b, s)
	ch.touch(f)
	ch.Summary = append(ch.Summary, fmt.Sprintf("add %s in %s", s.Name, f.Path))

	if s.Account != nil {
		if err := inv.Store.SetLink(s.Name, *s.Account); err != nil {
			ch.Warnings = append(ch.Warnings, "could not record the account link: "+err.Error())
		}
	}
	if s.ProxyJump != nil && *s.ProxyJump != "" {
		ch.Warnings = append(ch.Warnings, inv.jumpWarnings(s.Name, *s.ProxyJump)...)
	}
	return ch, nil
}

// Edit changes an existing host.
func (inv *Inventory) Edit(name string, s Spec, force bool) (*Change, error) {
	b := inv.Block(name)
	if b == nil {
		return nil, fmt.Errorf("no host named %q", name)
	}
	cur, _ := inv.Host(name)
	if cur.ReadOnly && !force {
		return nil, fmt.Errorf("%w: %s (%s); pass --force to write it anyway", ErrReadOnly, name, cur.File)
	}

	ch := &Change{inv: inv}
	set, unset, warn := inv.directivesFor(cur, s)
	ch.Warnings = append(ch.Warnings, warn...)

	if len(set) > 0 || len(unset) > 0 {
		sshconf.SetDirectives(b, set, unset)
		b = inv.reblock(cur.File, name)
	}
	if b != nil {
		applyMarkers(b, s)
	}
	if len(s.Aliases) > 0 {
		if b = inv.reblock(cur.File, name); b != nil {
			sshconf.SetPatterns(b, append([]string{cur.Name}, s.Aliases...))
		}
	}

	ch.touch(inv.Config.File(cur.File))
	ch.Summary = append(ch.Summary, "edit "+name)

	if s.Account != nil {
		if err := inv.Store.SetLink(name, *s.Account); err != nil {
			ch.Warnings = append(ch.Warnings, "could not record the account link: "+err.Error())
		}
	}
	if s.ProxyJump != nil && *s.ProxyJump != "" {
		ch.Warnings = append(ch.Warnings, inv.jumpWarnings(name, *s.ProxyJump)...)
	}
	return ch, nil
}

// reblock finds a stanza again after an edit rebuilt the block index.
func (inv *Inventory) reblock(path, name string) *sshconf.Block {
	f := inv.Config.File(path)
	if f == nil {
		return nil
	}
	for _, b := range f.Blocks {
		for _, n := range b.Names() {
			if strings.EqualFold(n, name) {
				return b
			}
		}
	}
	return nil
}

// applyMarkers writes the group, description and tag comment lines, which are
// the facts tram keeps in ssh_config that ssh itself has no keyword for.
func applyMarkers(b *sshconf.Block, s Spec) {
	if s.Group == nil && s.Desc == nil && s.Tags == nil {
		return
	}
	f := b.File
	indent := "    "
	for _, l := range b.Body() {
		if l.Kind == sshconf.LineDirective {
			indent = l.Indent
			break
		}
	}

	// Read what is there before removing it. The two markers share one rewrite,
	// so changing the group must not take the description with it: a field the
	// caller said nothing about is a field to leave alone.
	cur := meta{}
	for _, l := range append(append([]sshconf.Line{}, b.LeadLines()...), b.Body()...) {
		if m, ok := readMarker(l.Raw); ok {
			if m.group != "" {
				cur.group = m.group
			}
			if m.desc != "" {
				cur.desc = m.desc
			}
			if m.tags != "" {
				cur.tags = m.tags
			}
		}
	}

	// Clear the old metadata from both places it can live: inside the stanza,
	// where tram writes it, and in the comment run above the Host line, where
	// another tool may have. Missing the second would leave the old value sitting
	// above the new one, and the reader takes the last it sees.
	var keepLead []sshconf.Line
	for _, l := range b.LeadLines() {
		if !isMarker(l.Raw) {
			keepLead = append(keepLead, l)
		}
	}
	if len(keepLead) != len(b.LeadLines()) {
		sshconf.ReplaceLead(b, keepLead)
		if nb := reblockAt(f, b.Names()); nb != nil {
			b = nb
		}
	}

	var keep []sshconf.Line
	for _, l := range b.Body() {
		if isMarker(l.Raw) {
			continue
		}
		keep = append(keep, l)
	}

	group, desc, tags := cur.group, cur.desc, cur.tags
	if s.Group != nil {
		// Tidied on the way in, so that "prod / web" and "prod/web" are the same
		// group rather than two that only look alike in a listing.
		group = importer.NormaliseGroup(*s.Group)
	}
	if s.Desc != nil {
		desc = *s.Desc
	}
	if s.Tags != nil {
		tags = strings.Join(model.ParseTags(*s.Tags), ", ")
	}

	var head []sshconf.Line
	if group != "" {
		head = append(head, sshconf.RawLine(f, indent+groupMarker+" "+group))
	}
	if desc != "" {
		head = append(head, sshconf.RawLine(f, indent+descMarker+" "+desc))
	}
	if tags != "" {
		head = append(head, sshconf.RawLine(f, indent+tagsMarker+" "+tags))
	}
	sshconf.ReplaceBody(b, append(head, keep...))
}

// Clone copies a host under a new name, with overrides applied on top.
func (inv *Inventory) Clone(src, dst string, s Spec) (*Change, error) {
	from, ok := inv.Host(src)
	if !ok {
		return nil, fmt.Errorf("no host named %q", src)
	}
	if _, exists := inv.Host(dst); exists {
		return nil, fmt.Errorf("host %q already exists", dst)
	}
	if err := validName(dst); err != nil {
		return nil, err
	}

	base := Spec{Name: dst}
	base.HostName = Str(from.HostName)
	base.User = Str(from.User)
	base.Port = Str(from.Port)
	base.ProxyJump = Str(from.ProxyJump)
	base.Group = Str(from.Group)
	base.Desc = Str(from.Desc)
	base.Tags = Str(from.TagList())
	base.SetIdentityFiles(from.IdentityFiles)
	for kw, vals := range from.Other {
		for _, v := range vals {
			base.Extra = append(base.Extra, sshconf.Directive{Keyword: kw, Args: strings.Fields(v)})
		}
	}
	sort.Slice(base.Extra, func(i, j int) bool { return base.Extra[i].Keyword < base.Extra[j].Keyword })

	// Overrides win over the copied values.
	if s.HostName != nil {
		base.HostName = s.HostName
	}
	if s.User != nil {
		base.User = s.User
	}
	if s.Port != nil {
		base.Port = s.Port
	}
	if s.ProxyJump != nil {
		base.ProxyJump = s.ProxyJump
	}
	if s.Group != nil {
		base.Group = s.Group
	}
	if s.Desc != nil {
		base.Desc = s.Desc
	}
	if s.Tags != nil {
		base.Tags = s.Tags
	}
	if s.setIdentity {
		base.SetIdentityFiles(s.IdentityFiles)
	}
	base.Aliases = s.Aliases

	acct := from.Account
	if s.Account != nil {
		acct = *s.Account
	}
	if acct != "" {
		base.Account = Str(acct)
	}
	return inv.Add(base)
}

// Remove deletes a host stanza.
//
// Before deleting a station it names the hosts whose ProxyJump points at it,
// because those are the connections that stop working, and a warning after the
// fact is no use.
func (inv *Inventory) Remove(name string, force bool) (*Change, error) {
	b := inv.Block(name)
	if b == nil {
		return nil, fmt.Errorf("no host named %q", name)
	}
	h, _ := inv.Host(name)
	if h.ReadOnly && !force {
		return nil, fmt.Errorf("%w: %s (%s); pass --force to write it anyway", ErrReadOnly, name, h.File)
	}

	ch := &Change{inv: inv}
	if deps := model.Dependents(name, inv.hosts); len(deps) > 0 {
		ch.Warnings = append(ch.Warnings, fmt.Sprintf(
			"%s is a jump station for %s; those hosts will have a broken ProxyJump",
			name, strings.Join(model.Names(deps), ", ")))
	}
	f := b.File
	if len(b.Names()) > 1 {
		sshconf.RemovePattern(b, name)
	} else {
		sshconf.RemoveHost(b)
	}
	ch.touch(f)
	ch.Summary = append(ch.Summary, "remove "+name)
	_ = inv.Store.SetLink(name, "")
	return ch, nil
}

// Rename changes a host's name and follows the change through every ProxyJump
// that referred to it, across every file in the tree.
func (inv *Inventory) Rename(oldName, newName string, force bool) (*Change, error) {
	b := inv.Block(oldName)
	if b == nil {
		return nil, fmt.Errorf("no host named %q", oldName)
	}
	if _, exists := inv.Host(newName); exists {
		return nil, fmt.Errorf("host %q already exists", newName)
	}
	if err := validName(newName); err != nil {
		return nil, err
	}
	h, _ := inv.Host(oldName)
	if h.ReadOnly && !force {
		return nil, fmt.Errorf("%w: %s (%s); pass --force to write it anyway", ErrReadOnly, oldName, h.File)
	}

	ch := &Change{inv: inv}
	patterns := append([]string{newName}, h.Aliases...)
	sshconf.SetPatterns(b, patterns)
	ch.touch(b.File)
	ch.Summary = append(ch.Summary, fmt.Sprintf("rename %s to %s", oldName, newName))

	for _, dep := range model.Dependents(oldName, inv.hosts) {
		db := inv.Block(dep.Name)
		if db == nil {
			continue
		}
		if dep.ReadOnly && !force {
			ch.Warnings = append(ch.Warnings, fmt.Sprintf(
				"%s still points at %s but lives in %s, which is read-only; run with --force to update it",
				dep.Name, oldName, dep.File))
			continue
		}
		sshconf.SetDirectives(db, []sshconf.Directive{
			sshconf.D("ProxyJump", model.RenameJump(dep.ProxyJump, oldName, newName)),
		}, nil)
		ch.touch(db.File)
		ch.Summary = append(ch.Summary, fmt.Sprintf("update ProxyJump of %s", dep.Name))
	}

	_ = inv.Store.RenameLink(oldName, newName)
	_ = inv.Store.RenameHistory(oldName, newName)
	_ = inv.Store.RenameFacts(oldName, newName)
	return ch, nil
}

// jumpWarnings checks a new ProxyJump value for the two mistakes that are easy
// to make and hard to diagnose: a station that does not exist, and a loop.
func (inv *Inventory) jumpWarnings(host, jump string) []string {
	var out []string
	for _, spec := range model.SplitJump(jump) {
		hop := model.ParseJumpSpec(spec)
		// A station given as an address or a domain name is a perfectly ordinary
		// thing to write, so only a bare name that resolves to no stanza and
		// looks like nothing else is worth mentioning.
		if _, ok := inv.Host(hop.Host); !ok && !looksLikeAddress(hop.Host) {
			out = append(out, fmt.Sprintf("ProxyJump names %q, which is not a configured host", hop.Host))
		}
		if model.WouldCycle(host, hop.Host, inv.Lookup) {
			out = append(out, fmt.Sprintf(
				"ProxyJump through %q forms a loop; ssh would hang rather than report an error", hop.Host))
		}
	}
	return out
}

// looksLikeAddress reports whether a jump station names a machine directly
// rather than a stanza, which a dotted name or an address does.
func looksLikeAddress(s string) bool {
	return strings.Contains(s, ".") || strings.Contains(s, ":")
}

// validName rejects host names that ssh would read as something else.
func validName(n string) error {
	switch {
	case n == "":
		return errors.New("a host needs a name")
	case strings.ContainsAny(n, " \t\"'#"):
		return fmt.Errorf("host name %q contains whitespace or a quoting character", n)
	case strings.ContainsAny(n, "*?!"):
		return fmt.Errorf("host name %q contains a wildcard; tram manages named hosts, not patterns", n)
	}
	return nil
}

// ---- init -----------------------------------------------------------------

// InitResult describes what init did or would do.
type InitResult struct {
	ManagedPath  string
	Created      bool
	IncludeAdded bool
	Change       *Change
}

// Init sets up tram's own configuration file and makes ssh read it.
//
// The Include line goes at the very top of ssh_config, because ssh keeps the
// first value it sees for most keywords: an Include placed below a `Host *`
// stanza would be shadowed by it for every host.
func (inv *Inventory) Init(sshDir string) (*InitResult, error) {
	if sshDir == "" {
		sshDir = store.SSHDir()
	}
	managed := filepath.Join(sshDir, "config.d", store.ManagedName)
	res := &InitResult{ManagedPath: managed}

	if _, err := os.Stat(managed); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Dir(managed), err)
		}
		header := "# Managed by tram. Hand edits are kept, but tram writes new hosts here.\n"
		if err := os.WriteFile(managed, []byte(header), 0o600); err != nil {
			return nil, fmt.Errorf("create %s: %w", managed, err)
		}
		res.Created = true
	}

	root := inv.Config.Root
	for _, l := range root.Lines {
		if l.Is("Include") && strings.Contains(strings.Join(l.Args, " "), store.ManagedName) {
			return res, nil
		}
	}

	ch := &Change{inv: inv}
	sshconf.InsertLineAt(root, 0, "")
	sshconf.InsertLineAt(root, 0, store.ManagedInclude)
	sshconf.InsertLineAt(root, 0, "# Added by tram: keeps tram-managed hosts in their own file.")
	ch.touch(root)
	ch.Summary = append(ch.Summary, "add Include to "+root.Path)
	res.IncludeAdded = true
	res.Change = ch
	return res, nil
}

// reblockAt finds a stanza again by any of its names, after an edit rebuilt the
// block index and invalidated the pointer.
func reblockAt(f *sshconf.File, names []string) *sshconf.Block {
	for _, b := range f.Blocks {
		for _, n := range b.Names() {
			for _, want := range names {
				if strings.EqualFold(n, want) {
					return b
				}
			}
		}
	}
	return nil
}
