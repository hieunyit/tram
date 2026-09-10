// Package secret answers the questions ssh asks during a session, and forgets
// the answers when the session ends.
//
// There is no keyring, no vault and no stored password. A key passphrase you
// type is held for as long as tram is running, so the other hosts sharing that
// key file do not ask again, and it is gone when tram exits.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Environment variables that connect tram to the copy of itself ssh runs as an
// askpass helper. They carry a file path and the key that opens it, never a
// secret, and both die with the process that made them.
const (
	envFile = "TRAM_SESSION_FILE"
	envKey  = "TRAM_SESSION_KEY"
)

// Session is one run of tram: the passphrases typed during it, and nothing
// beyond it.
//
// The helper ssh runs is a separate process, so the answers cannot simply live
// in memory. They live in a file that only this run can read: the file holds
// ciphertext, the key exists only in this process and in the environment of the
// children it starts, and the file is deleted on the way out. A crash leaves
// bytes nobody can decrypt rather than a passphrase on disk.
type Session struct {
	path string
	key  []byte
}

// OpenSession prepares a session in dir. No file is written until something is
// actually cached, so a run that never meets an encrypted key leaves no trace.
func OpenSession(dir string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	sweepStale(dir)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate a session key: %w", err)
	}
	name := make([]byte, 8)
	if _, err := rand.Read(name); err != nil {
		return nil, fmt.Errorf("name the session: %w", err)
	}
	return &Session{
		path: filepath.Join(dir, "session-"+hex.EncodeToString(name)+".bin"),
		key:  key,
	}, nil
}

// Env returns the variables a child needs to reach this session.
func (s *Session) Env() []string {
	if s == nil {
		return nil
	}
	return []string{
		envFile + "=" + s.path,
		envKey + "=" + hex.EncodeToString(s.key),
	}
}

// Close removes the session's file. It is safe to call more than once, and on a
// session that never wrote anything.
func (s *Session) Close() {
	if s == nil {
		return
	}
	_ = os.Remove(s.path)
	for i := range s.key {
		s.key[i] = 0
	}
}

// sessionFromEnv rebuilds the session inside the askpass helper.
func sessionFromEnv() *Session {
	path, keyHex := os.Getenv(envFile), os.Getenv(envKey)
	if path == "" || keyHex == "" {
		return nil
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil
	}
	return &Session{path: path, key: key}
}

// sweepStale removes session files left behind by a run that did not exit
// cleanly. They are already unreadable, so this is tidying rather than
// security, but a state directory that fills up with dead files is its own
// small bug.
func sweepStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "session-") || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ---- the cache itself -----------------------------------------------------

// Get returns a cached passphrase for a key file.
func (s *Session) Get(keyPath string) (string, bool) {
	if s == nil {
		return "", false
	}
	entries := s.load()
	v, ok := entries[normaliseKeyPath(keyPath)]
	return v, ok
}

// Put caches a passphrase for a key file, for the rest of this run.
func (s *Session) Put(keyPath, passphrase string) error {
	if s == nil {
		return nil
	}
	entries := s.load()
	entries[normaliseKeyPath(keyPath)] = passphrase
	return s.save(entries)
}

// Count reports how many passphrases the session is holding.
func (s *Session) Count() int {
	if s == nil {
		return 0
	}
	return len(s.load())
}

func (s *Session) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Session) load() map[string]string {
	entries := map[string]string{}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return entries
	}
	aead, err := s.aead()
	if err != nil || len(raw) < aead.NonceSize() {
		return entries
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		// Written by a different run, whose key is gone. Not an error: it just
		// means nothing is cached for this one.
		return entries
	}
	_ = json.Unmarshal(plain, &entries)
	return entries
}

func (s *Session) save(entries map[string]string) error {
	plain, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	aead, err := s.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nonce, nonce, plain, nil)

	// Written through a temporary file so a second ssh asking at the same moment
	// never reads a half-written one.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tram-session-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

// normaliseKeyPath brings the several ways of spelling one key file to a single
// form, so that the `~/.ssh/id_ed25519` a configuration says and the absolute
// path ssh names in its prompt are recognised as the same key.
func normaliseKeyPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"`)
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
