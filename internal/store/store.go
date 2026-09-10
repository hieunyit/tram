package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/hieuny/tram/internal/model"
)

// Store is tram's own state, loaded once per run and saved on change.
type Store struct {
	mu sync.Mutex

	Accounts accountsFile
	History  historyFile
	Snippets snippetsFile
	Options  Options
}

type accountsFile struct {
	Accounts []model.Account `json:"accounts"`
	// Links maps a host name to the account it was created from. The link lives
	// here rather than in ssh_config because ssh has no notion of it, and
	// because losing it costs nothing but a label.
	Links map[string]string `json:"links"`
}

type historyFile struct {
	LastUsed  map[string]int64 `json:"last_used"`
	Favorites []string         `json:"favorites"`
}

// Snippet is a command kept for reuse across hosts.
type Snippet struct {
	Name    string `json:"name"`
	Desc    string `json:"desc,omitempty"`
	Command string `json:"command"`
	// Confirm marks a snippet that must be acknowledged before it runs, for the
	// ones that restart services or delete things.
	Confirm bool `json:"confirm,omitempty"`
}

type snippetsFile struct {
	Snippets []Snippet `json:"snippets"`
}

// Options are tram's own settings, kept in config.toml.
type Options struct {
	// WindowCommand overrides how tram opens a new terminal window. The token
	// {{host}} is replaced with the host name and nothing else is passed, which
	// keeps quoting out of the picture.
	WindowCommand []string `toml:"window_command"`
	// Terminal forces a terminal family instead of detecting one: wt, tmux,
	// iterm, apple, gnome, konsole, xterm.
	Terminal string `toml:"terminal"`
	// Parallel is the default fan-out for exec and ping.
	Parallel int `toml:"parallel"`
	// Timeout is the default per-host timeout in seconds.
	Timeout int `toml:"timeout"`
	// ASCII forces the plain box-drawing and marker set for old consoles.
	ASCII bool `toml:"ascii"`
	// ConfirmMulti asks before running a command on more than one host.
	ConfirmMulti bool `toml:"confirm_multi"`
	// ReusePassphrase lets tram hold a key passphrase for the rest of the run,
	// so the other hosts sharing that key file do not ask again. It is never
	// written anywhere and is gone when tram exits. Set it to false to leave
	// every prompt to ssh.
	ReusePassphrase bool `toml:"reuse_passphrase"`
}

// DefaultOptions are the settings a fresh install runs with.
func DefaultOptions() Options {
	return Options{Parallel: 5, Timeout: 10, ConfirmMulti: true, ReusePassphrase: true}
}

// Load reads every state file. A missing or unreadable file is not an error:
// tram degrades to defaults rather than refusing to start over its own cache.
func Load() *Store {
	s := &Store{Options: DefaultOptions()}
	s.Accounts.Links = map[string]string{}
	s.History.LastUsed = map[string]int64{}

	readJSON(path("accounts.json"), &s.Accounts)
	readJSON(path("history.json"), &s.History)
	readJSON(path("snippets.json"), &s.Snippets)
	if s.Accounts.Links == nil {
		s.Accounts.Links = map[string]string{}
	}
	if s.History.LastUsed == nil {
		s.History.LastUsed = map[string]int64{}
	}
	if b, err := os.ReadFile(path("config.toml")); err == nil {
		_ = toml.Unmarshal(b, &s.Options)
	}
	if s.Options.Parallel <= 0 {
		s.Options.Parallel = 5
	}
	if s.Options.Timeout <= 0 {
		s.Options.Timeout = 10
	}
	return s
}

func readJSON(p string, v any) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, v)
}

func writeJSON(p string, v any) error {
	if err := ensureDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tram-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// ---- accounts -------------------------------------------------------------

// AccountList returns the accounts sorted by name.
func (s *Store) AccountList() []model.Account {
	out := append([]model.Account(nil), s.Accounts.Accounts...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Account looks an account up by name.
func (s *Store) Account(name string) (model.Account, bool) {
	for _, a := range s.Accounts.Accounts {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return model.Account{}, false
}

// PutAccount adds or replaces an account.
func (s *Store) PutAccount(a model.Account) error {
	s.mu.Lock()
	replaced := false
	for i, e := range s.Accounts.Accounts {
		if strings.EqualFold(e.Name, a.Name) {
			s.Accounts.Accounts[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		s.Accounts.Accounts = append(s.Accounts.Accounts, a)
	}
	s.mu.Unlock()
	return s.saveAccounts()
}

// DeleteAccount removes an account and every link to it.
func (s *Store) DeleteAccount(name string) error {
	s.mu.Lock()
	var keep []model.Account
	for _, a := range s.Accounts.Accounts {
		if !strings.EqualFold(a.Name, name) {
			keep = append(keep, a)
		}
	}
	s.Accounts.Accounts = keep
	for h, acc := range s.Accounts.Links {
		if strings.EqualFold(acc, name) {
			delete(s.Accounts.Links, h)
		}
	}
	s.mu.Unlock()
	return s.saveAccounts()
}

// LinkOf returns the account a host is linked to.
func (s *Store) LinkOf(host string) string { return s.Accounts.Links[strings.ToLower(host)] }

// LinkedHosts returns the hosts linked to an account.
func (s *Store) LinkedHosts(account string) []string {
	var out []string
	for h, a := range s.Accounts.Links {
		if strings.EqualFold(a, account) {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// SetLink links a host to an account, or clears the link when account is empty.
func (s *Store) SetLink(host, account string) error {
	s.mu.Lock()
	if account == "" {
		delete(s.Accounts.Links, strings.ToLower(host))
	} else {
		s.Accounts.Links[strings.ToLower(host)] = account
	}
	s.mu.Unlock()
	return s.saveAccounts()
}

// RenameLink follows a host through a rename so its account link survives.
func (s *Store) RenameLink(oldName, newName string) error {
	s.mu.Lock()
	if a, ok := s.Accounts.Links[strings.ToLower(oldName)]; ok {
		delete(s.Accounts.Links, strings.ToLower(oldName))
		s.Accounts.Links[strings.ToLower(newName)] = a
	}
	s.mu.Unlock()
	return s.saveAccounts()
}

func (s *Store) saveAccounts() error { return writeJSON(path("accounts.json"), &s.Accounts) }

// ---- history --------------------------------------------------------------

// Touch records that a host was just connected to.
func (s *Store) Touch(host string) error {
	s.mu.Lock()
	s.History.LastUsed[strings.ToLower(host)] = time.Now().Unix()
	s.mu.Unlock()
	return writeJSON(path("history.json"), &s.History)
}

// LastUsed returns the Unix time a host was last connected to, or zero.
func (s *Store) LastUsed(host string) int64 { return s.History.LastUsed[strings.ToLower(host)] }

// IsFavorite reports whether a host is pinned.
func (s *Store) IsFavorite(host string) bool {
	for _, f := range s.History.Favorites {
		if strings.EqualFold(f, host) {
			return true
		}
	}
	return false
}

// ToggleFavorite pins or unpins a host and reports the new state.
func (s *Store) ToggleFavorite(host string) (bool, error) {
	s.mu.Lock()
	var keep []string
	found := false
	for _, f := range s.History.Favorites {
		if strings.EqualFold(f, host) {
			found = true
			continue
		}
		keep = append(keep, f)
	}
	if !found {
		keep = append(keep, host)
	}
	s.History.Favorites = keep
	s.mu.Unlock()
	return !found, writeJSON(path("history.json"), &s.History)
}

// RenameHistory follows a host through a rename.
func (s *Store) RenameHistory(oldName, newName string) error {
	s.mu.Lock()
	if t, ok := s.History.LastUsed[strings.ToLower(oldName)]; ok {
		delete(s.History.LastUsed, strings.ToLower(oldName))
		s.History.LastUsed[strings.ToLower(newName)] = t
	}
	for i, f := range s.History.Favorites {
		if strings.EqualFold(f, oldName) {
			s.History.Favorites[i] = newName
		}
	}
	s.mu.Unlock()
	return writeJSON(path("history.json"), &s.History)
}

// ---- snippets -------------------------------------------------------------

// SnippetList returns the snippets sorted by name.
func (s *Store) SnippetList() []Snippet {
	out := append([]Snippet(nil), s.Snippets.Snippets...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snippet looks a snippet up by name.
func (s *Store) Snippet(name string) (Snippet, bool) {
	for _, sn := range s.Snippets.Snippets {
		if strings.EqualFold(sn.Name, name) {
			return sn, true
		}
	}
	return Snippet{}, false
}

// PutSnippet adds or replaces a snippet.
func (s *Store) PutSnippet(sn Snippet) error {
	s.mu.Lock()
	replaced := false
	for i, e := range s.Snippets.Snippets {
		if strings.EqualFold(e.Name, sn.Name) {
			s.Snippets.Snippets[i] = sn
			replaced = true
			break
		}
	}
	if !replaced {
		s.Snippets.Snippets = append(s.Snippets.Snippets, sn)
	}
	s.mu.Unlock()
	return writeJSON(path("snippets.json"), &s.Snippets)
}

// DeleteSnippet removes a snippet by name.
func (s *Store) DeleteSnippet(name string) error {
	s.mu.Lock()
	var keep []Snippet
	for _, sn := range s.Snippets.Snippets {
		if !strings.EqualFold(sn.Name, name) {
			keep = append(keep, sn)
		}
	}
	s.Snippets.Snippets = keep
	s.mu.Unlock()
	return writeJSON(path("snippets.json"), &s.Snippets)
}

// SeedSnippets installs a small starter set the first time snippets are used.
func (s *Store) SeedSnippets() error {
	if len(s.Snippets.Snippets) > 0 {
		return nil
	}
	s.Snippets.Snippets = []Snippet{
		{Name: "disk", Desc: "Disk usage", Command: "df -h"},
		{Name: "mem", Desc: "Memory usage", Command: "free -m"},
		{Name: "load", Desc: "Uptime and load average", Command: "uptime"},
		{Name: "listen", Desc: "Listening sockets", Command: "ss -tlnp 2>/dev/null || netstat -tlnp"},
		{Name: "top-cpu", Desc: "Top processes by CPU", Command: "ps -eo pcpu,pid,user,comm --sort=-pcpu | head -11"},
		{Name: "reboot", Desc: "Reboot the machine", Command: "sudo reboot", Confirm: true},
	}
	return writeJSON(path("snippets.json"), &s.Snippets)
}

// ---- options --------------------------------------------------------------

// SaveOptions writes config.toml.
func (s *Store) SaveOptions() error {
	if err := ensureDir(); err != nil {
		return err
	}
	f, err := os.Create(path("config.toml"))
	if err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(s.Options)
}
