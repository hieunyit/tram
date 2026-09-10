package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// inventoryHints marks the files worth importing, so that a directory holding
// one inventory and forty other things does not read as forty-one equal
// choices.
var inventoryHints = map[string]string{
	".ini": "ansible inventory", ".yml": "ansible inventory", ".yaml": "ansible inventory",
	".csv": "spreadsheet export", ".tsv": "spreadsheet export",
}

// inventoryNames are the names an Ansible inventory carries when it has no
// extension at all, which is the usual case.
var inventoryNames = map[string]string{
	"hosts": "ansible inventory", "inventory": "ansible inventory",
}

// openFileBrowser lists a directory and lets one file be chosen from it.
//
// Typing a path is still there for anyone who knows it, but a path typed from
// memory is the one thing in this form that can be wrong in a way the form
// cannot tell you about until you submit. Walking to the file cannot be.
func (m *Model) openFileBrowser(dir string, onPick func(string) tea.Cmd) {
	dir = expandDir(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A directory that cannot be read is not a dead end: go up to one that
		// can, rather than dropping the user back into an empty form.
		parent := filepath.Dir(dir)
		if parent != dir {
			m.openFileBrowser(parent, onPick)
			return
		}
		m.problem = err.Error()
		return
	}

	var dirs, files []choice
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		if e.IsDir() {
			dirs = append(dirs, choice{value: full + string(filepath.Separator), label: name + "/"})
			continue
		}
		files = append(files, choice{value: full, label: name, note: fileHint(name)})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].label) < strings.ToLower(dirs[j].label) })
	sort.Slice(files, func(i, j int) bool {
		// The files that can actually be imported come first; the rest are
		// still listed, because tram guessing wrong should not hide your file.
		if (files[i].note != "") != (files[j].note != "") {
			return files[i].note != ""
		}
		return strings.ToLower(files[i].label) < strings.ToLower(files[j].label)
	})

	items := make([]choice, 0, len(dirs)+len(files)+1)
	if parent := filepath.Dir(dir); parent != dir {
		items = append(items, choice{value: parent + string(filepath.Separator), label: "../", note: "up"})
	}
	items = append(items, dirs...)
	items = append(items, files...)

	m.showPicker("open  "+dir, items, func(v string) tea.Cmd {
		if strings.HasSuffix(v, string(filepath.Separator)) {
			m.openFileBrowser(strings.TrimSuffix(v, string(filepath.Separator)), onPick)
			return nil
		}
		return onPick(v)
	})
	m.picker.help = "type to filter  enter opens a folder or picks a file  esc back to the form"
}

// fileHint labels a file tram knows how to read.
func fileHint(name string) string {
	if h, ok := inventoryHints[strings.ToLower(filepath.Ext(name))]; ok {
		return h
	}
	if h, ok := inventoryNames[strings.ToLower(name)]; ok {
		return h
	}
	return ""
}

// expandDir turns what is in the field into a directory to list: a '~' becomes
// home, a path to a file becomes the directory holding it, and anything empty
// or nonsensical becomes the working directory.
func expandDir(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if p == "" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		home, _ := os.UserHomeDir()
		return home
	}
	if info, err := os.Stat(p); err == nil {
		if info.IsDir() {
			return p
		}
		return filepath.Dir(p)
	}
	// A half-typed path: list the deepest part of it that exists.
	if parent := filepath.Dir(p); parent != p {
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			return parent
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

var _ = fmt.Sprintf
