package launcher

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Terminal names a family of terminal programs tram knows how to open a new
// window or tab in.
type Terminal string

const (
	TermWindowsTerminal Terminal = "wt"
	// TermConhost is Windows without Windows Terminal: the console host every
	// Windows has, opened through cmd's own start command.
	TermConhost Terminal = "start"
	TermTmux    Terminal = "tmux"
	TermITerm   Terminal = "iterm"
	TermApple   Terminal = "apple"
	TermGnome   Terminal = "gnome"
	TermKonsole Terminal = "konsole"
	TermXterm   Terminal = "xterm"
	TermGeneric Terminal = "generic"
	TermNone    Terminal = ""
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
	// Debian and its descendants keep whichever terminal is installed behind
	// this name, which is the last thing worth trying before giving up.
	{"x-terminal-emulator", TermGeneric},
}

// graphical reports whether this session could put a window on a screen at all.
// Over ssh, or on a server with no display, the answer is no and no amount of
// looking for terminal programs will change it.
func graphical() bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return true
	}
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

// InsideWindowsTerminal reports whether tram is running in a Windows Terminal
// window, as opposed to merely on a machine that has one installed.
//
// The difference decides where a new tab lands. Windows Terminal will happily
// open a tab in a window belonging to somebody else's session, which from where
// you are sitting looks exactly like nothing happening.
func InsideWindowsTerminal() bool { return os.Getenv("WT_SESSION") != "" }

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
		// Every Windows can open a console window, with or without anything
		// installed. There is no such thing as a Windows with no way to open
		// one, so tram should never say there is.
		return TermConhost
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
	if !graphical() {
		return "this session has no display, so there is no window to open. " +
			"Run tram inside tmux and W opens a tmux window instead."
	}
	return "no terminal program tram recognises. Install one, run tram inside " +
		"tmux, or set window_command in config.toml using {{host}} for the name."
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
		// -w 0 means the window most recently used, which is the right answer
		// only when that window is the one tram is drawing in. Run from any
		// other terminal, it puts the tab in an application the user is not
		// looking at, so there a window of its own is the honest thing.
		where := "new"
		if InsideWindowsTerminal() {
			where = "0"
		}
		return []string{"wt.exe", "-w", where, "new-tab", "--title", host, self, host}, nil
	case TermConhost:
		// start tells a title from a program by the quotes around it, and Go
		// quotes an argument only when it holds a space. An unquoted host name
		// was therefore read as the program to run, and Windows answered that
		// it could not find it. The trailing space is what puts the quotes
		// there, and the title is what they were for.
		return []string{"cmd.exe", "/c", "start", host + " ", self, host}, nil
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

// SplitCommand builds the command that opens host beside whatever is already on
// screen, rather than in a tab of its own.
//
// Only two terminals can do this: Windows Terminal and tmux. Everywhere else the
// caller is told so and given a window command instead, because a key that does
// nothing is worse than a key that does the next best thing.
func SplitCommand(t Terminal, self, host string, override []string) (argv []string, split bool, err error) {
	switch t {
	case TermWindowsTerminal:
		// A pane is beside something. Beside what, if tram is not in that
		// window at all? So this one asks where it is before it offers.
		if !InsideWindowsTerminal() {
			break
		}
		return []string{"wt.exe", "-w", "0", "split-pane", "--title", host, self, host}, true, nil
	case TermTmux:
		return []string{"tmux", "split-window", "-h", self + " " + host}, true, nil
	}
	argv, err = WindowCommand(t, self, host, override)
	return argv, false, err
}

// OpenQuietly launches a window or a pane without letting it write anything to
// this terminal, and gives it a moment to fail.
//
// The interface is drawing on the alternate screen while this runs, so a line
// of chatter from wt.exe would land in the middle of the host list. It is still
// read: a launcher that failed used to do so in silence, and the interface said
// a tab had opened when none had.
func OpenQuietly(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no window command")
	}
	var said bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = &said, &said

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		if msg := strings.TrimSpace(said.String()); msg != "" {
			return fmt.Errorf("%s: %s", argv[0], firstLine(msg))
		}
		return fmt.Errorf("%s: %w", argv[0], err)
	case <-time.After(1500 * time.Millisecond):
		// Still running. A terminal that stays in the foreground is one that
		// opened, so this is the good ending.
		go func() { <-done }()
		return nil
	}
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i > 0 {
		return s[:i]
	}
	return s
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
