package secret

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// TTYAvailable reports whether a secret can be asked for on the console.
//
// The caller checks this before it starts ssh, not after. Once ssh is running
// with SSH_ASKPASS_REQUIRE=force it will not fall back to prompting on its own,
// so a helper that turns out to have nowhere to ask would simply fail the
// authentication with nothing on screen to explain why.
func TTYAvailable() bool {
	in, out, err := openTTY()
	if err != nil {
		return false
	}
	ok := term.IsTerminal(int(in.Fd()))
	in.Close()
	if out != in {
		out.Close()
	}
	return ok
}

// AskOnTTY asks for a secret on the console and returns what was typed. The
// text is never echoed.
func AskOnTTY(prompt string) (string, error) {
	in, out, err := openTTY()
	if err != nil {
		return "", err
	}
	defer func() {
		in.Close()
		if out != in {
			out.Close()
		}
	}()

	fmt.Fprint(out, prompt)
	b, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("read from the console: %w", err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("nothing entered")
	}
	return v, nil
}

// AskOnTTYVisible puts a question on the console and returns the answer, with
// the typing shown.
//
// It is for the host key question, whose answer is "yes", "no" or a
// fingerprint. None of those is a secret, and hiding what you type while asking
// you to compare a fingerprint would be its own small cruelty.
func AskOnTTYVisible(prompt string) (string, error) {
	in, out, err := openTTY()
	if err != nil {
		return "", err
	}
	defer func() {
		in.Close()
		if out != in {
			out.Close()
		}
	}()

	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read from the console: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// NoteOnTTY writes a line to the console, for telling the user what tram just
// remembered. It goes to the console rather than to standard error because in
// askpass mode standard error belongs to ssh.
func NoteOnTTY(format string, args ...any) {
	in, out, err := openTTY()
	if err != nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	fmt.Fprintf(out, format+"\n", args...)
	in.Close()
	if out != in {
		out.Close()
	}
}
