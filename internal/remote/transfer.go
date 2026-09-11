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
// The remote path is quoted twice on purpose. scp hands its remote argument to
// a shell on the far end, so a name with a space in it arrives as two names
// unless it carries quotes of its own, and the quotes have to survive the local
// shell first. This is scp's oldest sharp edge.
func (c Copy) Argv(configPath string) []string {
	argv := []string{"scp"}
	if configPath != "" {
		argv = append(argv, "-F", configPath)
	}
	if c.Dir {
		argv = append(argv, "-r")
	}
	// -p keeps the timestamps, -q keeps the progress meter out of a screen tram
	// is drawing on.
	argv = append(argv, "-p", "-q")

	far := c.Host + ":" + Quote(c.Remote)
	if c.Up {
		return append(argv, c.Local, far)
	}
	return append(argv, far, c.Local)
}

// Run performs the copy and returns whatever scp complained about.
func (c Copy) Run(configPath string, env []string) error {
	argv := c.Argv(configPath)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := firstComplaint(strings.Split(string(out), "\n")); msg != "no answer" {
		return fmt.Errorf("%s", msg)
	}
	return err
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
