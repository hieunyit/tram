// Package importer reads host inventories written for other tools and turns
// them into records tram can write into ssh_config.
//
// It reads, and does not write. Nothing here touches a configuration file: the
// caller decides what to do with the records, which is what makes a preview
// and a dry run possible without a second code path.
package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Format names a supported inventory layout.
type Format string

const (
	// AnsibleINI is the classic Ansible inventory: sections, host lines and
	// key=value variables.
	AnsibleINI Format = "ansible-ini"
	// AnsibleYAML is the YAML inventory, with nested children.
	AnsibleYAML Format = "ansible-yaml"
	// CSV is a spreadsheet export with a header row.
	CSV Format = "csv"
)

// Formats lists what can be imported, for help text.
func Formats() []string { return []string{string(AnsibleINI), string(AnsibleYAML), string(CSV)} }

// Record is one host found in a source file, already mapped onto the fields
// tram writes. Everything is a string because that is what goes into
// ssh_config; the importer does not invent types the file did not have.
type Record struct {
	Name      string `json:"name"`
	HostName  string `json:"hostname"`
	User      string `json:"user"`
	Port      string `json:"port"`
	Key       string `json:"key"`
	ProxyJump string `json:"proxy_jump"`
	Group     string `json:"group"`
	Desc      string `json:"desc"`

	// Groups are every group the source put this host in. tram keeps one, and
	// this is here so the command can say which ones it dropped.
	Groups []string `json:"groups"`
	// Where names the line the record came from, for error messages.
	Where string `json:"where"`
}

// Result is everything one source file yielded.
type Result struct {
	Format   Format   `json:"format"`
	Path     string   `json:"path"`
	Records  []Record `json:"records"`
	Warnings []string `json:"warnings"`
	// Skipped names hosts the source described but that cannot be reached over
	// ssh, such as an Ansible host with a local connection.
	Skipped []string `json:"skipped"`
}

// Options adjusts how a file is read.
type Options struct {
	// Format overrides detection.
	Format Format
	// GroupPrefix is prepended to every group, so an inventory can be filed
	// under a heading of its own.
	GroupPrefix string
	// FlatGroups turns off the nesting built from Ansible's children sections,
	// keeping only the group the host is directly in.
	FlatGroups bool
}

// Parse reads a file and returns the hosts it describes.
func Parse(path string, opt Options) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	f := opt.Format
	if f == "" {
		f = Detect(path, data)
	}

	var res *Result
	switch f {
	case AnsibleINI:
		res, err = parseINI(data, opt)
	case AnsibleYAML:
		res, err = parseYAML(data, opt)
	case CSV:
		res, err = parseCSV(data, opt)
	default:
		return nil, fmt.Errorf("cannot tell what kind of file %s is; pass --format with one of %s",
			filepath.Base(path), strings.Join(Formats(), ", "))
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	res.Format, res.Path = f, path

	if opt.GroupPrefix != "" {
		prefix := strings.Trim(opt.GroupPrefix, "/")
		for i := range res.Records {
			if res.Records[i].Group == "" {
				res.Records[i].Group = prefix
				continue
			}
			res.Records[i].Group = prefix + "/" + res.Records[i].Group
		}
	}
	return res, nil
}

// Detect guesses the format from the file's name and its first lines.
//
// The extension is only a hint: Ansible inventories are routinely called
// `hosts` with no extension at all, so the content decides when it can.
func Detect(path string, data []byte) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv":
		return CSV
	case ".yml", ".yaml":
		return AnsibleYAML
	case ".ini":
		return AnsibleINI
	}

	head := string(data)
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, raw := range strings.Split(head, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[") && strings.Contains(line, "]"):
			return AnsibleINI
		case strings.HasPrefix(line, "---"):
			return AnsibleYAML
		case strings.HasSuffix(line, ":"):
			return AnsibleYAML
		case strings.Count(line, ",") >= 1 && !strings.Contains(line, "="):
			return CSV
		default:
			// A bare host line with key=value variables is the INI form.
			return AnsibleINI
		}
	}
	return ""
}

// ansibleVar maps an Ansible variable onto the ssh_config field it corresponds
// to. Only the variables that describe how to reach a machine are mapped;
// everything else in an inventory is about what Ansible does once it gets
// there, and is none of tram's business.
func ansibleVar(r *Record, key, value string) bool {
	value = unquote(value)
	switch strings.ToLower(key) {
	case "ansible_host", "ansible_ssh_host":
		r.HostName = value
	case "ansible_user", "ansible_ssh_user":
		r.User = value
	case "ansible_port", "ansible_ssh_port":
		r.Port = value
	case "ansible_ssh_private_key_file", "ansible_private_key_file":
		r.Key = value
	case "ansible_ssh_common_args", "ansible_ssh_extra_args":
		if j := jumpFromArgs(value); j != "" {
			r.ProxyJump = j
		}
	default:
		return false
	}
	return true
}

// jumpFromArgs pulls a jump host out of the raw ssh arguments an inventory
// carries, which is where Ansible users put one.
//
// Both spellings appear in the wild: the -J flag, and a ProxyJump given with
// -o. A ProxyCommand is deliberately not translated, because a command is not
// a host and guessing at one would produce a stanza that looks right and
// connects somewhere else.
func jumpFromArgs(args string) string {
	fields := strings.Fields(args)
	for i, f := range fields {
		switch {
		case f == "-J" && i+1 < len(fields):
			return unquote(fields[i+1])
		case strings.HasPrefix(f, "-J"):
			return unquote(strings.TrimPrefix(f, "-J"))
		case f == "-o" && i+1 < len(fields):
			if v, ok := cutPrefixFold(unquote(fields[i+1]), "proxyjump="); ok {
				return v
			}
		case strings.HasPrefix(f, "-oProxyJump="):
			return unquote(strings.TrimPrefix(f, "-oProxyJump="))
		}
	}
	return ""
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// unreachable reports whether an Ansible host is one ssh cannot be pointed at,
// such as an entry that runs locally or through a container runtime.
func unreachable(vars map[string]string) (string, bool) {
	c := strings.ToLower(unquote(vars["ansible_connection"]))
	switch c {
	case "", "ssh", "smart", "paramiko", "paramiko_ssh":
		return "", false
	}
	return c, true
}

// validName reports whether a name can be used as a Host pattern. Names with
// whitespace, quoting characters or wildcards are rejected rather than escaped,
// because ssh would read them as something other than one machine.
func validName(n string) bool {
	return n != "" && !strings.ContainsAny(n, " \t\"'#*?!")
}
