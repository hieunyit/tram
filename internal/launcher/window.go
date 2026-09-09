package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Terminal names a family of terminal programs tram knows how to open a new
// window or tab in.
type Terminal string

const (
	TermWindowsTerminal Terminal = "wt"
	TermTmux            Terminal = "tmux"
	TermITerm           Terminal = "iterm"
	TermApple           Terminal = "apple"
	TermGnome           Terminal = "gnome"
	TermKonsole         Terminal = "konsole"
	TermXterm           Terminal = "xterm"
	TermNone            Terminal = ""
)

// DetectTerminal works out which terminal tram is running inside, from the
// environment the terminal itself sets.
func DetectTerminal() Terminal {
	if os.Getenv("TMUX") != "" {
		return TermTmux
	}
	switch runtime.GOOS {
	case "windows":
		if os.Getenv("WT_SESSION") != "" {
			return TermWindowsTerminal
		}
		if _, err := exec.LookPath("wt.exe"); err == nil {
			return TermWindowsTerminal
		}
	case "darwin":
		if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
			return TermITerm
		}
		return TermApple
	default:
		if os.Getenv("KONSOLE_VERSION") != "" {
			return TermKonsole
		}
		for _, c := range []string{"gnome-terminal", "xterm"} {
			if _, err := exec.LookPath(c); err == nil {
				if c == "gnome-terminal" {
					return TermGnome
				}
				return TermXterm
			}
		}
	}
	return TermNone
}

// WindowCommand builds the command that opens host in a new window or tab.
//
// Only the host name is ever passed to the terminal, never a full ssh command
// line. Windows Terminal in particular treats semicolons and quotes inside its
// arguments as its own syntax, and an ssh invocation carrying a ProxyCommand
// would be mangled beyond repair. Re-invoking tram with one short name sidesteps
// the whole problem: the child process rebuilds the real command itself.
func WindowCommand(t Terminal, self, host string, override []string) ([]string, error) {
	if len(override) > 0 {
		out := make([]string, len(override))
		for i, a := range override {
			out[i] = strings.ReplaceAll(strings.ReplaceAll(a, "{{host}}", host), "{{tram}}", self)
		}
		return out, nil
	}

	switch t {
	case TermWindowsTerminal:
		return []string{"wt.exe", "-w", "0", "new-tab", "--title", host, self, host}, nil
	case TermTmux:
		return []string{"tmux", "new-window", "-n", host, self + " " + host}, nil
	case TermITerm, TermApple:
		script := fmt.Sprintf(`tell application "Terminal" to do script "%s %s"`, self, host)
		return []string{"osascript", "-e", script}, nil
	case TermGnome:
		return []string{"gnome-terminal", "--title", host, "--", self, host}, nil
	case TermKonsole:
		return []string{"konsole", "-e", self, host}, nil
	case TermXterm:
		return []string{"xterm", "-T", host, "-e", self, host}, nil
	}
	return nil, fmt.Errorf("no terminal detected for --window; set window_command in config.toml")
}

// OpenWindow launches the new window and returns without waiting for it.
func OpenWindow(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no window command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
