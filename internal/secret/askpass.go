package secret

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ssh asks about a host key in several wordings across versions, but every
	// one of them offers a yes/no choice about trusting a fingerprint.
	hostKeyPrompt = regexp.MustCompile(`(?i)authenticity of host|continue connecting|fingerprint|host identification has changed|host key.*(changed|verification)`)
	// A passphrase prompt names the key file, which is what tells tram which
	// key an answer belongs to. Two wordings exist, one quoting the path and
	// one not, and the match is anchored on the prompt's trailing colon so that
	// a Windows path keeps its drive letter and loses nothing else.
	passphrasePrompt = regexp.MustCompile(`(?is)enter passphrase for(?: key)?\s+(?:'([^']*)'|(.*?))\s*:\s*$`)
	// A password prompt names the account it is for.
	passwordPrompt = regexp.MustCompile(`(?i)^(?:([^@\s]+)@)?([^@\s':]+)(?:'s)?\s+password`)
)

// IsAskpassInvocation reports whether this process was started by ssh asking a
// question rather than by a user running a command.
//
// The shape is unmistakable: exactly one argument, and it reads as a question.
// No host name contains a space or ends in a colon. Recognising it by shape
// rather than by an environment variable also means tram works as a general
// askpass helper, for ssh runs it did not start.
func IsAskpassInvocation(args []string) bool {
	return len(args) == 1 && LooksLikePrompt(args[0])
}

// LooksLikePrompt reports whether a string is one of the questions ssh asks
// through an askpass helper.
func LooksLikePrompt(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || !strings.ContainsAny(s, " \t") {
		return false
	}
	return passphrasePrompt.MatchString(s) ||
		passwordPrompt.MatchString(s) ||
		strings.Contains(strings.ToLower(s), "password") ||
		hostKeyPrompt.MatchString(s)
}

// Askpass answers one question from ssh and writes the answer to out.
//
// A key passphrase is served from this run's cache when it is already there,
// and otherwise asked for on the console and cached, so the next host using the
// same key file does not ask again. A password is asked for and not kept: tram
// stores no passwords anywhere, ever.
//
// A host key confirmation is refused, deliberately and permanently. Answering
// "yes" to an unknown fingerprint on the user's behalf would turn a warning
// about a possible interception into a silent accept.
func Askpass(prompt string, out io.Writer) error {
	if hostKeyPrompt.MatchString(prompt) {
		return relayHostKeyQuestion(prompt, out)
	}
	sess := sessionFromEnv()

	if key, ok := PassphrasePath(prompt); ok {
		if v, cached := sess.Get(key); cached {
			return write(out, v)
		}
		v, err := AskOnTTY(strings.TrimRight(prompt, " ") + " ")
		if err != nil {
			return err
		}
		if err := sess.Put(key, v); err != nil {
			// The session can still go ahead; only the reuse is lost.
			NoteOnTTY("tram could not hold on to that passphrase: %v", err)
		}
		return write(out, v)
	}

	if !strings.Contains(strings.ToLower(prompt), "password") {
		return fmt.Errorf("unrecognised prompt: %s", prompt)
	}
	// Asked, answered, forgotten.
	v, err := AskOnTTY(strings.TrimRight(prompt, " ") + " ")
	if err != nil {
		return err
	}
	return write(out, v)
}

// relayHostKeyQuestion puts ssh's question in front of the person and returns
// their answer unchanged.
//
// tram never decides this one. It also must not refuse it: with the helper
// forced, ssh does not fall back to asking on its own, so refusing means the
// first connection to any new host fails with "Host key verification failed"
// and no way to say yes. Relaying is not answering. The fingerprint is shown
// exactly as ssh wrote it, and whatever is typed goes straight back.
func relayHostKeyQuestion(prompt string, out io.Writer) error {
	text := strings.TrimRight(prompt, " ")
	if !strings.HasSuffix(text, "\n") {
		text += " "
	}
	answer, err := AskOnTTYVisible(text)
	if err != nil {
		return fmt.Errorf("nowhere to put ssh's host key question: %w", err)
	}
	// Nothing here is cached. The next new host gets asked about too.
	return write(out, answer)
}

// PassphrasePath returns the key file a passphrase prompt is about.
func PassphrasePath(prompt string) (string, bool) {
	m := passphrasePrompt.FindStringSubmatch(strings.TrimSpace(prompt))
	if m == nil {
		return "", false
	}
	key := m[1]
	if key == "" {
		key = m[2]
	}
	key = strings.TrimSpace(strings.Trim(key, `'"`))
	return key, key != ""
}

func write(out io.Writer, v string) error {
	_, err := io.WriteString(out, v+"\n")
	return err
}

// ---- ssh capability -------------------------------------------------------

var versionRe = regexp.MustCompile(`OpenSSH[_ ](\d+)\.(\d+)`)

// SSHVersion returns the version string of the ssh on PATH.
func SSHVersion() string {
	cmd := exec.Command("ssh", "-V")
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SupportsAskpassRequire reports whether this ssh honours
// SSH_ASKPASS_REQUIRE=force.
//
// It decides whether reusing a passphrase is possible at all. In tram's handoff
// mode ssh has a real terminal, and given the choice it asks on that terminal
// and ignores the helper entirely. Only SSH_ASKPASS_REQUIRE, added in OpenSSH
// 8.4, makes it use the helper anyway. On an older ssh tram does not arm the
// helper, and every prompt is ssh's own, exactly as if tram were not there.
func SupportsAskpassRequire() (bool, string) {
	v := SSHVersion()
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return false, v
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 8 || (major == 8 && minor >= 4), v
}

var _ = os.Getenv
