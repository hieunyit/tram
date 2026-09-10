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
	TermGeneric         Terminal = "generic"
	TermNone            Terminal = ""
)

// linuxTerminals are the emulators tram knows how to open a window in, most
// common first. Each takes its command after a flag that means "run this".
var linuxTerminals = []struct {
	bin  string
	term Terminal
}{
	{"gnome-terminal", TermGnome},
	{"konsole", TermKonsole},
	{"xfce4-terminal", TermGeneric},
	{"tilix", TermGeneric},
	{"terminator", TermGeneric},
	{"alacritty", TermGeneric},
	{"kitty", TermGeneric},
	{"wezterm", TermGeneric},
	{"foot", TermGeneric},
	{"urxvt", TermXterm},
	{"xterm", TermXterm},
}

// graphical reports whether this session could put a window on a screen at all.
// Over ssh, or on a server with no display, the answer is no and no amount of
// looking for terminal programs will change it.
func graphical() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// linuxTerminalBin returns the emulator found on PATH, if any.
func linuxTerminalBin() (string, Terminal, bool) {
	if want := os.Getenv("TERMINAL"); want != "" {
		if p, err := exec.LookPath(want); err == nil {
			return p, TermGeneric, true
		}
	}
	for _, t := range linuxTerminals {
		if p, err := exec.LookPath(t.bin); err == nil {
			return p, t.term, true
		}
	}
	return "", TermNone, false
}

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
		if !graphical() {
			return TermNone
		}
		if os.Getenv("KONSOLE_VERSION") != "" {
			return TermKonsole
		}
		if _, t, ok := linuxTerminalBin(); ok {
			return t
		}
	}
	return TermNone
}

// WhyNoWindow explains a failed detection in terms of what to do about it,
// which differs completely between "you are on a server" and "your terminal is
// one tram has not met".
func WhyNoWindow() string {
	if os.Getenv("TMUX") != "" {
		return "" // tmux can always make a window
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && !graphical() {
		return "this session has no display, so there is no window to open. " +
			"Run tram inside tmux and W opens a tmux window instead."
	}
	return "no terminal program tram recognises. " +
		"Set window_command in config.toml, using {{host}} where the host name goes."
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
	case TermGeneric:
		bin, _, ok := linuxTerminalBin()
		if !ok {
			break
		}
		// -e is the one flag every one of these accepts for "run this".
		return []string{bin, "-e", self, host}, nil
	case TermKonsole:
		return []string{"konsole", "-e", self, host}, nil
	case TermXterm:
		return []string{"xterm", "-T", host, "-e", self, host}, nil
	}
	return nil, fmt.Errorf("cannot open a window: %s", WhyNoWindow())
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
