package remote

import (
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// Transfers go through scp rather than down the shell this package keeps open.
//
// The open shell is for questions, which are small and want to be quick. A file
// is neither: sending one through a shell means encoding it, which doubles it,
// and a mistake in the encoding is a corrupted file rather than a wrong answer.
// scp is the program for this, it speaks the same ssh_config, and it is already
// on every machine that has ssh.

// Copy is one transfer, in either direction.
type Copy struct {
	// Host is the name in ssh_config, used for the remote half of the path.
	Host string
	// Local and Remote are the two ends. Up says which one is the source.
	Local  string
	Remote string
	Up     bool
	// Dir marks a directory, which scp needs telling about.
	Dir bool
}

// Argv builds the scp command line.
//
// The quoting of the far path depends on which protocol scp is speaking, and
// getting it backwards is the difference between a copied file and a complaint
// about a file whose name contains a quotation mark.
//
// OpenSSH 8.7 and later can be told to move the file over SFTP with -s, and
// there the path is taken exactly as written: quoting it would make the quotes
// part of the name. Older scp hands the path to a shell on the far side, where
// an unquoted space splits it in two and an unquoted $(...) is a command that
// runs on the host. So: -s and bare, or nothing and quoted, and never the two
// crossed over.
func (c Copy) Argv(configPath string, overSFTP bool) []string {
	argv := []string{"scp"}
	if overSFTP {
		argv = append(argv, "-s")
	}
	if configPath != "" {
		argv = append(argv, "-F", configPath)
	}
	if c.Dir {
		argv = append(argv, "-r")
	}
	// -p keeps the timestamps, -q keeps the progress meter out of a screen tram
	// is drawing on.
	argv = append(argv, "-p", "-q")

	remote := c.Remote
	if !overSFTP {
		remote = Quote(remote)
	}
	far := c.Host + ":" + remote
	if c.Up {
		return append(argv, c.Local, far)
	}
	return append(argv, far, c.Local)
}

// Run performs the copy and returns whatever scp complained about.
//
// It asks for the SFTP protocol first and falls back once, because the two
// protocols need opposite quoting and the only reliable way to know which scp
// is installed is to ask it.
func (c Copy) Run(configPath string, env []string) error {
	out, err := c.attempt(configPath, env, true)
	if err == nil {
		return nil
	}
	if looksUnsupported(out) {
		if _, err2 := c.attempt(configPath, env, false); err2 == nil {
			return nil
		} else {
			return err2
		}
	}
	if msg := firstComplaint(strings.Split(out, "\n")); msg != "no answer" {
		return fmt.Errorf("%s", msg)
	}
	return err
}

func (c Copy) attempt(configPath string, env []string, overSFTP bool) (string, error) {
	argv := c.Argv(configPath, overSFTP)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// looksUnsupported reports whether scp refused the flag rather than the file,
// which is how an older one answers -s.
func looksUnsupported(out string) bool {
	low := strings.ToLower(out)
	return strings.Contains(low, "unknown option") ||
		strings.Contains(low, "illegal option") ||
		strings.Contains(low, "invalid option") ||
		strings.HasPrefix(low, "usage:")
}

// Describe says what a transfer is about to do, for the line that asks whether
// to do it.
func (c Copy) Describe() string {
	if c.Up {
		return path.Base(filepath(c.Local)) + " " + arrow + " " + c.Host + ":" + c.Remote
	}
	return c.Host + ":" + c.Remote + " " + arrow + " " + c.Local
}

const arrow = "->"

// filepath turns a Windows path into something path.Base can read, which is the
// only place in tram where the two kinds of separator meet.
func filepath(p string) string { return strings.ReplaceAll(p, `\`, "/") }
