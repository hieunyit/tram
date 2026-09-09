package model

import "strings"

// AuthMethod is how an account proves who it is.
type AuthMethod string

const (
	// AuthKey uses a private key file, written as IdentityFile.
	AuthKey AuthMethod = "key"
	// AuthPassword uses a password held in the OS keyring and handed to ssh
	// through the askpass helper, never through the configuration file.
	AuthPassword AuthMethod = "password"
	// AuthAgent relies on whatever the ssh agent already holds.
	AuthAgent AuthMethod = "agent"
)

// Account is an identity: who you log in as and how you prove it. It carries no
// address, no port and no jump host. That separation is what keeps the model
// from growing into a second copy of ssh_config.
type Account struct {
	Name    string     `json:"name"`
	User    string     `json:"user"`
	Auth    AuthMethod `json:"auth"`
	KeyPath string     `json:"key_path,omitempty"`
	Desc    string     `json:"desc,omitempty"`
}

// Valid reports whether the account is complete enough to materialize.
func (a Account) Valid() error {
	switch {
	case strings.TrimSpace(a.Name) == "":
		return errf("account needs a name")
	case strings.TrimSpace(a.User) == "":
		return errf("account %q needs a user", a.Name)
	case a.Auth == AuthKey && a.KeyPath == "":
		return errf("account %q uses key authentication but has no key path", a.Name)
	case a.Auth != AuthKey && a.Auth != AuthPassword && a.Auth != AuthAgent:
		return errf("account %q has unknown auth method %q", a.Name, a.Auth)
	}
	return nil
}

// Materialized returns the User and IdentityFile values this account writes
// into a host stanza. Everything else about the host is left alone.
func (a Account) Materialized() (user string, identityFiles []string) {
	user = a.User
	if a.Auth == AuthKey && a.KeyPath != "" {
		identityFiles = []string{a.KeyPath}
	}
	return user, identityFiles
}
