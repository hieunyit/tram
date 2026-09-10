package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ExpandKeyPath resolves a key path the way ssh would, so that a path written
// as ~/.ssh/id_ed25519 in the configuration can be opened.
func ExpandKeyPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// KeyInfo describes a private key file.
type KeyInfo struct {
	Path        string
	Exists      bool
	Encrypted   bool
	Type        string
	Fingerprint string
	// PermsTooOpen is set when the file is readable by anyone but the owner,
	// which makes ssh refuse to use it.
	PermsTooOpen bool
}

// InspectKey reads a private key file and reports what tram can tell about it
// without knowing its passphrase.
func InspectKey(path string) (KeyInfo, error) {
	full := ExpandKeyPath(path)
	info := KeyInfo{Path: full}

	st, err := os.Stat(full)
	if err != nil {
		return info, fmt.Errorf("read key %s: %w", full, err)
	}
	info.Exists = true
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		info.PermsTooOpen = true
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return info, fmt.Errorf("read key %s: %w", full, err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err == nil {
		info.Type = signer.PublicKey().Type()
		info.Fingerprint = ssh.FingerprintSHA256(signer.PublicKey())
		return info, nil
	}
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		info.Encrypted = true
		if missing.PublicKey != nil {
			info.Type = missing.PublicKey.Type()
			info.Fingerprint = ssh.FingerprintSHA256(missing.PublicKey)
		}
		return info, nil
	}
	return info, fmt.Errorf("parse key %s: %w", full, err)
}

// VerifyPassphrase checks a passphrase against the key before it is stored.
//
// Storing an unverified passphrase is worse than storing nothing: the failure
// surfaces later, as an authentication error during a connection, where it
// looks like a server problem rather than a typo.
func VerifyPassphrase(path, passphrase string) error {
	full := ExpandKeyPath(path)
	data, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("read key %s: %w", full, err)
	}
	if _, err := ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase)); err != nil {
		if errors.Is(err, x509IncorrectPassword) || strings.Contains(strings.ToLower(err.Error()), "decrypt") {
			return fmt.Errorf("that passphrase does not open %s", full)
		}
		return fmt.Errorf("that passphrase does not open %s: %w", full, err)
	}
	return nil
}

// x509IncorrectPassword stands in for the sentinel the ssh package wraps when a
// passphrase is wrong, which differs between key formats.
var x509IncorrectPassword = errors.New("x509: decryption password incorrect")
