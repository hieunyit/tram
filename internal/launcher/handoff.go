package launcher

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// SSHError is the exit status ssh uses for its own failures, as opposed to a
// status the remote command produced.
const SSHError = 255

// Result is the outcome of a handed-over session.
type Result struct {
	// ExitCode is 255 when ssh itself failed and otherwise whatever the remote
	// command returned. Telling the two apart is the difference between "the
	// connection broke" and "the command you ran said no".
	ExitCode int
	// SSHFailed is true when the failure was ssh's rather than the command's.
	SSHFailed bool
	// Interrupted is true when the session was cut short by a signal rather
	// than by anything exiting: ctrl+c while ssh is still trying to connect,
	// most often. It is not a failure and there is nothing to report about it.
	Interrupted bool
	// Err is set when the ssh binary could not be started at all.
	Err error
}

// Handoff runs ssh with tram's own terminal, blocks until it exits and then
// puts the terminal back the way it was.
//
// Three things have to be right for a session to feel native. The child gets
// the real standard streams, so ssh sees a terminal and full-screen programs on
// the far side behave. Interrupt signals are ignored by tram for the duration,
// so ctrl+c reaches the remote shell instead of killing tram. And the console
// mode is captured before the child starts and restored after it exits, because
// a program that leaves the console in raw mode makes every later command in
// that window unusable.
func Handoff(argv []string, env []string) Result {
	if len(argv) == 0 {
		return Result{ExitCode: SSHError, SSHFailed: true, Err: errors.New("empty command")}
	}

	restore, err := saveTerminal()
	if err == nil {
		defer restore()
	}

	// While ssh owns the terminal, tram must not react to the keys the user is
	// typing at the remote side. Ignoring these signals is what lets ctrl+c and
	// suspend reach ssh rather than ending tram.
	stop := ignoreJobSignals()
	defer stop()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if env != nil {
		cmd.Env = env
	}
	configureChild(cmd)

	if err := cmd.Start(); err != nil {
		return Result{ExitCode: SSHError, SSHFailed: true, Err: err}
	}
	err = cmd.Wait()
	if err == nil {
		return Result{}
	}

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		// A process killed by a signal has no exit code, and the standard
		// library reports -1. Calling that "the remote command exited with
		// status -1" is wrong twice over: nothing exited, and the reason was a
		// key the user pressed.
		if code < 0 {
			return Result{ExitCode: 130, Interrupted: true}
		}
		return Result{ExitCode: code, SSHFailed: code == SSHError}
	}
	return Result{ExitCode: SSHError, SSHFailed: true, Err: err}
}

// ignoreJobSignals stops tram reacting to the terminal's job-control signals
// for as long as a child owns the terminal.
func ignoreJobSignals() func() {
	sigs := jobSignals()
	if len(sigs) == 0 {
		return func() {}
	}
	ch := make(chan os.Signal, 8)
	signal.Notify(ch, sigs...)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				// Swallow it. The child has the terminal and the operating
				// system delivered the same signal to it directly.
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
