package sshconf

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sshG asks ssh itself what configuration it would use for a host. This is the
// oracle the writer is checked against: not what the parser believes, but what
// the program that actually reads the file believes.
func sshG(t *testing.T, confPath, host string, env ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("ssh", "-F", confPath, "-G", host)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	var keep []string
	for _, l := range lines {
		// Drop settings that depend on the machine rather than the file, so a
		// difference in the diff always means a difference tram caused.
		if strings.HasPrefix(l, "userknownhostsfile ") || strings.HasPrefix(l, "identityfile ~") {
			continue
		}
		if strings.TrimSpace(l) != "" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n"), true
}

func requireSSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not on PATH; differential test needs the real client")
	}
}

// hostsIn lists the concrete host names a file declares, which is the set the
// differential test walks. A few invented names are added so that the wildcard
// stanzas and the global preamble are exercised too.
func hostsIn(f *File) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			out = append(out, n)
		}
	}
	for _, b := range f.Blocks {
		for _, n := range b.Names() {
			add(n)
		}
	}
	add("prod-web")
	add("some-unlisted-host")
	add("host.example.com")
	return out
}

// TestDifferentialSSHG rewrites every corpus file through tram and asserts that
// ssh resolves every host to exactly the same effective configuration
// afterwards. A parser bug that silently drops or reorders a directive shows up
// here even when the round-trip test is happy.
func TestDifferentialSSHG(t *testing.T) {
	requireSSH(t)

	for _, src := range corpus(t) {
		src := src
		t.Run(filepath.Base(src), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			raw, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			f := Parse(raw, path)
			hosts := hostsIn(f)

			before := map[string]string{}
			for _, h := range hosts {
				if g, ok := sshG(t, path, h); ok {
					before[h] = g
				}
			}
			if len(before) == 0 {
				t.Skip("ssh could not read this file at all; nothing to compare")
			}

			// Exercise the write path rather than just re-emitting the bytes:
			// add a stanza, then remove it again. The effective configuration
			// of every pre-existing host must come out unchanged.
			if _, err := AddHost(f, []string{"tram-differential-probe"}, []Directive{D("HostName", "127.0.0.99"), D("Port", "2222")}, "temporary"); err != nil {
				t.Fatal(err)
			}
			if b := findBlock(f, "tram-differential-probe"); b != nil {
				RemoveHost(b)
			}
			if err := f.Save(SaveOptions{}); err != nil {
				t.Fatal(err)
			}

			for _, h := range hosts {
				want, ok := before[h]
				if !ok {
					continue
				}
				got, ok := sshG(t, path, h)
				if !ok {
					t.Errorf("ssh -G %s failed after rewrite", h)
					continue
				}
				if got != want {
					t.Errorf("effective config for %s changed after rewrite:\n%s", h, UnifiedDiff(h, []byte(want), []byte(got)))
				}
			}
		})
	}
}

// TestDifferentialSSHGAfterEdit checks the other direction: an edit tram makes
// on purpose must land in ssh's view exactly as asked, and must not disturb any
// other host in the file.
func TestDifferentialSSHGAfterEdit(t *testing.T) {
	requireSSH(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	src := `Host *
    ServerAliveInterval 60

Host web1
    HostName 10.0.0.1
    User deploy

Host web2
    HostName 10.0.0.2
    User deploy
    Port 22
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	otherBefore, _ := sshG(t, path, "web2")

	f := Parse([]byte(src), path)
	SetDirectives(findBlock(f, "web1"), []Directive{D("Port", "2222"), D("User", "root")}, nil)
	if err := f.Save(SaveOptions{}); err != nil {
		t.Fatal(err)
	}

	got, ok := sshG(t, path, "web1")
	if !ok {
		t.Fatal("ssh -G web1 failed")
	}
	for _, want := range []string{"port 2222", "user root", "hostname 10.0.0.1", "serveraliveinterval 60"} {
		if !strings.Contains(got, want) {
			t.Errorf("ssh does not see %q after the edit:\n%s", want, got)
		}
	}
	if otherAfter, _ := sshG(t, path, "web2"); otherAfter != otherBefore {
		t.Errorf("editing web1 changed web2:\n%s", UnifiedDiff("web2", []byte(otherBefore), []byte(otherAfter)))
	}
}

// TestDifferentialInclude checks that a host reached through an Include is
// found, edited in the file that actually declares it, and still resolves the
// same way for ssh.
//
// The Include path is relative to the ssh directory rather than absolute. That
// is not a stylistic choice: OpenSSH for Windows silently ignores an Include
// whose path starts with a drive letter, so tram never writes one.
func TestDifferentialInclude(t *testing.T) {
	requireSSH(t)

	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	sub := filepath.Join(sshDir, "config.d", "tram.conf")
	if err := os.MkdirAll(filepath.Dir(sub), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(sshDir, "config")

	if err := os.WriteFile(sub, []byte("Host inner\n    HostName 10.9.9.9\n    User sub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootSrc := "Include config.d/tram.conf\n\nHost outer\n    HostName 10.0.0.1\n"
	if err := os.WriteFile(root, []byte(rootSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home}

	cfg, err := Load(root, LoadOptions{Home: home, BaseDir: sshDir})
	if err != nil {
		t.Fatal(err)
	}
	if errs := cfg.Errors(); len(errs) > 0 {
		t.Fatalf("load reported %v", errs)
	}
	b := cfg.FindHost("inner")
	if b == nil {
		t.Fatal("host behind an Include was not found")
	}
	if b.File.Path != sub {
		t.Fatalf("host resolved to %s, it is declared in %s", b.File.Path, sub)
	}
	if before, ok := sshG(t, root, "inner", env...); !ok || !strings.Contains(before, "hostname 10.9.9.9") {
		t.Fatalf("ssh did not follow the Include to begin with:\n%s", before)
	}

	SetDirectives(b, []Directive{D("Port", "2200")}, nil)
	if err := b.File.Save(SaveOptions{Backup: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub + BackupSuffix); err != nil {
		t.Errorf("no backup written next to the edited file: %v", err)
	}

	got, ok := sshG(t, root, "inner", env...)
	if !ok {
		t.Fatal("ssh -G inner failed")
	}
	for _, want := range []string{"hostname 10.9.9.9", "user sub", "port 2200"} {
		if !strings.Contains(got, want) {
			t.Errorf("ssh does not see %q:\n%s", want, got)
		}
	}
	if rootAfter, _ := os.ReadFile(root); string(rootAfter) != rootSrc {
		t.Errorf("the root file was touched when editing an included host")
	}
}
