package remote

import (
	"strings"
	"testing"
)

// TestArgvQuotesForTheRightProtocol is the bug a real session found.
//
// scp speaks two protocols and they need opposite quoting. Since OpenSSH 8.7 it
// moves the file over SFTP, where the path is taken exactly as written, so a
// quoted path became a file whose name contained quotation marks and nothing
// was ever uploaded. The older protocol hands the path to a shell on the far
// side, where the quotes are what keeps a space from splitting it in two.
func TestArgvQuotesForTheRightProtocol(t *testing.T) {
	job := Copy{Host: "web1", Local: `C:\Users\hellc\notes.txt`, Remote: "/var/tmp/notes.txt", Up: true}

	sftp := job.Argv("", true)
	if !has(sftp, "-s") {
		t.Error("the sftp attempt does not ask for the sftp protocol")
	}
	if got := sftp[len(sftp)-1]; got != "web1:/var/tmp/notes.txt" {
		t.Errorf("the far end is %q; over sftp the path is taken as written", got)
	}

	legacy := job.Argv("", false)
	if has(legacy, "-s") {
		t.Error("the fallback still asks for a protocol the old scp does not know")
	}
	if got := legacy[len(legacy)-1]; got != `web1:'/var/tmp/notes.txt'` {
		t.Errorf("the far end is %q; the old protocol needs it quoted for the remote shell", got)
	}
}

// TestArgvPutsTheEndsInTheRightOrder covers the direction, which is the other
// thing a transfer can get exactly backwards.
func TestArgvPutsTheEndsInTheRightOrder(t *testing.T) {
	up := Copy{Host: "web1", Local: "/tmp/a", Remote: "/var/a", Up: true}.Argv("", true)
	if up[len(up)-2] != "/tmp/a" || up[len(up)-1] != "web1:/var/a" {
		t.Errorf("an upload reads %v", up[len(up)-2:])
	}

	down := Copy{Host: "web1", Local: "/tmp/a", Remote: "/var/a"}.Argv("", true)
	if down[len(down)-2] != "web1:/var/a" || down[len(down)-1] != "/tmp/a" {
		t.Errorf("a download reads %v", down[len(down)-2:])
	}
}

// TestADirectoryIsCopiedRecursively checks the one flag without which a folder
// is refused rather than copied.
func TestADirectoryIsCopiedRecursively(t *testing.T) {
	argv := Copy{Host: "web1", Local: "/tmp/logs", Remote: "/var/logs", Up: true, Dir: true}.Argv("", true)
	if !has(argv, "-r") {
		t.Errorf("a directory is being copied without -r: %v", argv)
	}
}

// TestTheConfigFileIsPassedOn keeps transfers reading the same ssh_config the
// rest of tram was pointed at.
func TestTheConfigFileIsPassedOn(t *testing.T) {
	argv := Copy{Host: "web1", Local: "a", Remote: "b", Up: true}.Argv("/tmp/other_config", true)
	if !strings.Contains(strings.Join(argv, " "), "-F /tmp/other_config") {
		t.Errorf("the configuration file is not passed on: %v", argv)
	}
}

// TestAnOldScpIsRecognised covers how the fallback decides it is needed: a
// refused flag is retried, a refused file is reported.
func TestAnOldScpIsRecognised(t *testing.T) {
	for _, out := range []string{
		"scp: unknown option -- s",
		"usage: scp [-346BCpqrTv] [-c cipher] ...",
		"illegal option -- s",
	} {
		if !looksUnsupported(out) {
			t.Errorf("%q was not read as an scp that refuses -s", out)
		}
	}
	for _, out := range []string{
		"scp: /var/tmp/notes.txt: No such file or directory",
		"Permission denied (publickey).",
		"",
	} {
		if looksUnsupported(out) {
			t.Errorf("%q was read as a refused flag; it is a refused file", out)
		}
	}
}

func has(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}
