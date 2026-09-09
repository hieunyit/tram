//go:build !windows

package launcher

import (
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// saveTerminal captures the terminal's attributes and returns a function that
// restores them, so a session that exits badly does not leave the shell in raw
// mode with no echo.
func saveTerminal() (func(), error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, nil
	}
	state, err := term.GetState(fd)
	if err != nil {
		return func() {}, err
	}
	return func() { _ = term.Restore(fd, state) }, nil
}

// jobSignals lists the signals tram ignores while ssh owns the terminal, so
// that ctrl+c, ctrl+z and a window resize all reach ssh instead.
func jobSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU}
}

// configureChild puts ssh in tram's own process group so the terminal driver
// treats it as the foreground job.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}
}

var _ = signal.Ignore
