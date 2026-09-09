// Package store holds tram's own state: accounts, snippets, history and
// options. Nothing here is required for tram to work. Deleting the whole
// directory costs you Recent, Favorites, snippets and account links; the host
// list itself lives in ssh_config and is untouched.
package store

import (
	"os"
	"path/filepath"
	"runtime"
)

// Dir returns tram's configuration directory, honouring TRAM_HOME first so
// that tests and portable installs can redirect it.
func Dir() string {
	if d := os.Getenv("TRAM_HOME"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if ad := os.Getenv("APPDATA"); ad != "" {
			return filepath.Join(ad, "tram")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "tram")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tram"
	}
	return filepath.Join(home, ".config", "tram")
}

// SSHDir returns the directory ssh reads its user configuration from.
func SSHDir() string {
	if d := os.Getenv("TRAM_SSH_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh"
	}
	return filepath.Join(home, ".ssh")
}

// ConfigPath is the ssh_config tram reads as the root of the tree.
func ConfigPath() string {
	if p := os.Getenv("TRAM_SSH_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(SSHDir(), "config")
}

// ManagedPath is the file tram init creates and tram writes new hosts into.
const ManagedName = "tram.conf"

// ManagedPath returns the full path of tram's own configuration file.
func ManagedPath() string { return filepath.Join(SSHDir(), "config.d", ManagedName) }

// ManagedInclude is the Include line tram init puts at the top of ssh_config.
//
// The path is relative on purpose. OpenSSH for Windows silently ignores an
// Include whose path begins with a drive letter, so an absolute path here
// would produce a file that looks right and does nothing.
const ManagedInclude = "Include config.d/" + ManagedName

func path(name string) string { return filepath.Join(Dir(), name) }

func ensureDir() error { return os.MkdirAll(Dir(), 0o700) }
