//go:build windows

package launcher

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// saveTerminal captures the console mode of both the input and the output
// handle and returns a function that puts them back.
//
// This is not belt and braces. Older conhost windows are routinely left in raw
// mode after a full-screen program exits, and a terminal library that restores
// only what it changed does not help when the program that changed it was ssh
// running in the same console. tram therefore records the mode itself, around
// the child, and restores it unconditionally.
func saveTerminal() (func(), error) {
	in := windows.Handle(os.Stdin.Fd())
	out := windows.Handle(os.Stdout.Fd())

	var inMode, outMode uint32
	inOK := windows.GetConsoleMode(in, &inMode) == nil
	outOK := windows.GetConsoleMode(out, &outMode) == nil
	if !inOK && !outOK {
		return func() {}, nil
	}
	return func() {
		if inOK {
			_ = windows.SetConsoleMode(in, inMode)
		}
		if outOK {
			_ = windows.SetConsoleMode(out, outMode)
		}
	}, nil
}

// jobSignals lists the signals to ignore while a child owns the console. On
// Windows only the interrupt signals exist, and the console delivers them to
// every process attached to the console, the child included.
func jobSignals() []os.Signal { return []os.Signal{os.Interrupt} }

// configureChild leaves the child in tram's own console so that it inherits the
// window, its size and its input.
func configureChild(cmd *exec.Cmd) {}
