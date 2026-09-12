package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIdentityFilesReadsWhatSshWouldOffer is the bug two real sessions found in
// two different shapes: a key that tram could not see.
//
// tram's parser reads a host's own stanza. ssh reads that, plus every Host *
// block above it, plus its own defaults when nothing names a key at all. A host
// with a passphrase-protected key therefore looked to tram like a host with no
// key, so nothing was done about the passphrase until ssh stopped to ask for it
// on the screen the interface was drawing on.
func TestIdentityFilesReadsWhatSshWouldOffer(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh on this machine to ask")
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	// The key is named nowhere near the host: it belongs to the Host * block,
	// which is exactly the case tram used to miss.
	body := "Host *\n    IdentityFile " + filepath.ToSlash(filepath.Join(dir, "shared_key")) + "\n\n" +
		"Host web1\n    HostName 10.0.0.1\n"
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	files := IdentityFiles(config, "web1")
	if len(files) == 0 {
		t.Fatal("ssh named no keys at all")
	}
	joined := strings.Join(files, " ")
	if !strings.Contains(joined, "shared_key") {
		t.Errorf("the key from the Host * block is missing: %v", files)
	}
}

// TestIdentityFilesNamesTheDefaults covers the other shape: a configuration
// that names no key, where ssh still offers the ones in ~/.ssh.
func TestIdentityFilesNamesTheDefaults(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh on this machine to ask")
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, []byte("Host web1\n    HostName 10.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files := IdentityFiles(config, "web1")
	if len(files) == 0 {
		t.Fatal("a host with no key of its own was reported as offering none")
	}
	if !strings.Contains(strings.Join(files, " "), "id_") {
		t.Errorf("the defaults are missing from %v", files)
	}
}

// TestIdentityFilesSurvivesNoAnswer keeps a missing or unhappy ssh from being an
// error: the caller falls back to the stanza, which is what it used to use.
func TestIdentityFilesSurvivesNoAnswer(t *testing.T) {
	if got := IdentityFiles(filepath.Join(t.TempDir(), "nothing-here"), ""); got != nil {
		t.Errorf("an unanswerable question returned %v", got)
	}
}
