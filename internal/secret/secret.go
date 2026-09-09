// Package secret stores passwords and key passphrases where the operating
// system keeps such things, and hands them to ssh through the one channel that
// does not leak: the askpass helper.
//
// A value stored here never reaches ssh_config, never appears on a command
// line where another user could read it from the process table, and never sits
// in an environment variable that a child process would inherit.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// service is the name tram registers under in the operating system's keyring.
const service = "tram"

// ErrNotFound means no secret is stored for that subject.
var ErrNotFound = errors.New("no secret stored")

// Subject names what a secret belongs to. A password belongs to an identity
// rather than to a machine, because that is what it actually authenticates;
// a passphrase belongs to a key file.
type Subject string

// PasswordFor builds the subject for an account's or a host's password.
func PasswordFor(kind, name string) Subject {
	return Subject("password:" + kind + ":" + strings.ToLower(name))
}

// PassphraseFor builds the subject for a private key's passphrase.
func PassphraseFor(keyPath string) Subject {
	return Subject("passphrase:" + normaliseKeyPath(keyPath))
}

// Kind reports whether a subject is a password or a passphrase.
func (s Subject) Kind() string {
	if strings.HasPrefix(string(s), "passphrase:") {
		return "passphrase"
	}
	return "password"
}

// Label renders a subject for display, without revealing anything secret.
func (s Subject) Label() string {
	parts := strings.SplitN(string(s), ":", 3)
	switch {
	case len(parts) == 3:
		return parts[1] + " " + parts[2]
	case len(parts) == 2:
		return parts[1]
	}
	return string(s)
}

func normaliseKeyPath(p string) string {
	p = strings.Trim(p, `"`)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
}

// Store reads and writes secrets, preferring the operating system's keyring and
// falling back to an encrypted file when there is none.
type Store struct {
	dir string

	mu       sync.Mutex
	fallback *vault
}

// New returns a store keeping its fallback vault in dir.
func New(dir string) *Store { return &Store{dir: dir} }

// KeyringAvailable reports whether the operating system's own credential store
// can be used. When it cannot, tram says so at the moment you save a secret
// rather than at the moment you need one.
func KeyringAvailable() bool {
	const probe = "tram-availability-probe"
	if err := keyring.Set(service, probe, "x"); err != nil {
		return false
	}
	_ = keyring.Delete(service, probe)
	return true
}

// Backend names where a store is actually keeping secrets.
func (s *Store) Backend() string {
	if KeyringAvailable() {
		return "os keyring"
	}
	return "encrypted file at " + s.vaultPath()
}

// Set stores a secret.
func (s *Store) Set(sub Subject, value string) error {
	if value == "" {
		return errors.New("refusing to store an empty secret")
	}
	if err := keyring.Set(service, string(sub), value); err == nil {
		_ = s.forgetFallback(sub)
		return nil
	}
	return s.setFallback(sub, value)
}

// Get retrieves a secret.
func (s *Store) Get(sub Subject) (string, error) {
	v, err := keyring.Get(service, string(sub))
	if err == nil {
		return v, nil
	}
	if v, err := s.getFallback(sub); err == nil {
		return v, nil
	}
	return "", ErrNotFound
}

// Delete removes a secret from wherever it is.
func (s *Store) Delete(sub Subject) error {
	kerr := keyring.Delete(service, string(sub))
	ferr := s.forgetFallback(sub)
	if kerr != nil && ferr != nil {
		return ErrNotFound
	}
	return nil
}

// List returns the subjects tram knows about.
//
// The operating system's keyring cannot be enumerated portably, so tram keeps
// its own index of subject names. The index holds names only; the values stay
// where the operating system put them.
func (s *Store) List() ([]Subject, error) {
	idx, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	out := make([]Subject, 0, len(idx))
	for name := range idx {
		out = append(out, Subject(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// ---- subject index --------------------------------------------------------

func (s *Store) indexPath() string { return filepath.Join(s.dir, "secrets.index.json") }
func (s *Store) vaultPath() string { return filepath.Join(s.dir, "secrets.enc") }
func (s *Store) keyPath() string   { return filepath.Join(s.dir, "device.key") }

func (s *Store) readIndex() (map[string]string, error) {
	idx := map[string]string{}
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}
	_ = json.Unmarshal(b, &idx)
	return idx, nil
}

func (s *Store) writeIndex(idx map[string]string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath(), append(b, '\n'), 0o600)
}

// Remember records that a subject exists, so that `secret ls` can show it.
func (s *Store) Remember(sub Subject, where string) error {
	idx, err := s.readIndex()
	if err != nil {
		return err
	}
	idx[string(sub)] = where
	return s.writeIndex(idx)
}

// Forget drops a subject from the index.
func (s *Store) Forget(sub Subject) error {
	idx, err := s.readIndex()
	if err != nil {
		return err
	}
	delete(idx, string(sub))
	return s.writeIndex(idx)
}

// Where reports which backend holds a subject, according to the index.
func (s *Store) Where(sub Subject) string {
	idx, _ := s.readIndex()
	return idx[string(sub)]
}

// ---- encrypted fallback ---------------------------------------------------

type vault struct {
	Entries map[string]string `json:"entries"` // subject -> base64 ciphertext
}

// deviceKey returns the key the fallback vault is encrypted with, creating it
// on first use. The key file is the whole secret, so it is written with the
// tightest permissions the platform offers and never leaves the machine.
func (s *Store) deviceKey() ([]byte, error) {
	p := s.keyPath()
	if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
		sum := sha256.Sum256(b)
		return sum[:], nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return nil, fmt.Errorf("create device key: %w", err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *Store) aead() (cipher.AEAD, error) {
	key, err := s.deviceKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Store) loadVault() (*vault, error) {
	if s.fallback != nil {
		return s.fallback, nil
	}
	v := &vault{Entries: map[string]string{}}
	if b, err := os.ReadFile(s.vaultPath()); err == nil {
		_ = json.Unmarshal(b, v)
		if v.Entries == nil {
			v.Entries = map[string]string{}
		}
	}
	s.fallback = v
	return v, nil
}

func (s *Store) saveVault(v *vault) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.vaultPath(), append(b, '\n'), 0o600)
}

func (s *Store) setFallback(sub Subject, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.aead()
	if err != nil {
		return err
	}
	v, err := s.loadVault()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ct := aead.Seal(nonce, nonce, []byte(value), []byte(sub))
	v.Entries[string(sub)] = base64.StdEncoding.EncodeToString(ct)
	return s.saveVault(v)
}

func (s *Store) getFallback(sub Subject) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.loadVault()
	if err != nil {
		return "", err
	}
	enc, ok := v.Entries[string(sub)]
	if !ok {
		return "", ErrNotFound
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	aead, err := s.aead()
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("stored secret is truncated")
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(sub))
	if err != nil {
		return "", errors.New("stored secret could not be decrypted with this machine's key")
	}
	return string(pt), nil
}

func (s *Store) forgetFallback(sub Subject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.loadVault()
	if err != nil {
		return err
	}
	if _, ok := v.Entries[string(sub)]; !ok {
		return ErrNotFound
	}
	delete(v.Entries, string(sub))
	return s.saveVault(v)
}
