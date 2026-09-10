package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionReusesAPassphraseAcrossHosts is the whole point: type it for the
// first host, and every other host sharing that key file is silent.
func TestSessionReusesAPassphraseAcrossHosts(t *testing.T) {
	dir := t.TempDir()
	sess, err := OpenSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	key := filepath.Join(dir, "id_ed25519")
	if _, ok := sess.Get(key); ok {
		t.Fatal("a fresh session already held something")
	}
	if err := sess.Put(key, "typed once"); err != nil {
		t.Fatal(err)
	}

	// A second connection is a second process reading the same session.
	child := sessionFrom(t, sess.Env())
	v, ok := child.Get(key)
	if !ok {
		t.Fatal("the helper could not see what the session holds")
	}
	if v != "typed once" {
		t.Errorf("got %q", v)
	}

	// The same key written any of its usual ways is the same key.
	for _, spelling := range []string{key, strings.ToUpper(key[:1]) + key[1:], filepath.ToSlash(key)} {
		if _, ok := child.Get(spelling); !ok {
			t.Errorf("%q was not recognised as the same key file", spelling)
		}
	}
	// A different key is not.
	if _, ok := child.Get(filepath.Join(dir, "id_other")); ok {
		t.Error("a different key file read as cached")
	}
}

// TestSessionEndsWithTheRun is the other half of the promise: close it and the
// passphrase is gone, with nothing left on disk to find.
func TestSessionEndsWithTheRun(t *testing.T) {
	dir := t.TempDir()
	sess, err := OpenSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "id_ed25519")
	if err := sess.Put(key, "typed once"); err != nil {
		t.Fatal(err)
	}
	path := sess.path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}

	sess.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the session file outlived the session: %v", err)
	}

	// A new run starts empty even in the same directory.
	next, err := OpenSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, ok := next.Get(key); ok {
		t.Error("a new run inherited the previous one's passphrase")
	}
}

// TestSessionFileIsUselessWithoutItsKey covers what a crash leaves behind.
//
// Deleting the file on the way out is not enough on its own, because a process
// that dies does not delete anything. The bytes have to be worthless too.
func TestSessionFileIsUselessWithoutItsKey(t *testing.T) {
	dir := t.TempDir()
	sess, err := OpenSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	const secretText = "a passphrase that must not appear on disk"
	if err := sess.Put(filepath.Join(dir, "id_ed25519"), secretText); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(sess.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretText) {
		t.Error("the passphrase is on disk in the clear")
	}
	if strings.Contains(string(raw), "id_ed25519") {
		t.Error("the key path is on disk in the clear")
	}

	// A later run finds the file but not the key that opens it.
	orphan := &Session{path: sess.path, key: make([]byte, 32)}
	if v, ok := orphan.Get(filepath.Join(dir, "id_ed25519")); ok {
		t.Errorf("a file from a dead run was readable: %q", v)
	}
}

// TestNoSessionMeansNoCache checks that the helper degrades to asking every
// time rather than failing, when tram did not set a session up.
func TestNoSessionMeansNoCache(t *testing.T) {
	var nilSession *Session
	if _, ok := nilSession.Get("/x"); ok {
		t.Error("a session that does not exist returned something")
	}
	if err := nilSession.Put("/x", "y"); err != nil {
		t.Errorf("caching into no session should be a no-op, got %v", err)
	}
	if n := nilSession.Count(); n != 0 {
		t.Errorf("count = %d", n)
	}
	nilSession.Close() // must not panic
}

// sessionFrom rebuilds a session the way the askpass helper does, from the
// environment alone.
func sessionFrom(t *testing.T, env []string) *Session {
	t.Helper()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	s := sessionFromEnv()
	if s == nil {
		t.Fatal("the helper could not rebuild the session from its environment")
	}
	return s
}
