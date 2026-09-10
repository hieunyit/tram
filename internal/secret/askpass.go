package secret

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Environment variables ssh's askpass helper is started with. They carry names
// and a nonce, never a secret: the value itself only ever moves between the
// keyring and ssh's own pipe.
const (
	// EnvToken names the subject the helper should answer for.
	EnvToken = "TRAM_ASKPASS_TOKEN"
	// EnvLearn switches the helper into learn mode and carries the nonce the
	// captured secret is parked under until the session proves it was right.
	EnvLearn = "TRAM_ASKPASS_LEARN"
	// EnvHost names the host the session was opened for, which is how the
	// helper tells a prompt about the destination from one about a jump
	// station it passes through on the way.
	EnvHost = "TRAM_ASKPASS_HOST"
)

// IsAskpassInvocation reports whether this process was started by ssh asking a
// question rather than by a user running a command.
func IsAskpassInvocation() bool {
	return os.Getenv(EnvToken) != "" || os.Getenv(EnvLearn) != ""
}

var (
	// ssh asks about a host key in several wordings across versions, but every
	// one of them offers a yes/no choice about trusting a fingerprint.
	hostKeyPrompt = regexp.MustCompile(`(?i)authenticity of host|continue connecting|fingerprint|host key.*(changed|verification)`)
	// A passphrase prompt names the key file, which is what tells tram which
	// stored passphrase to hand back. Two wordings exist, one quoting the path
	// and one not, and the match is anchored on the prompt's trailing colon so
	// that a Windows path keeps its drive letter and loses nothing else.
	passphrasePrompt = regexp.MustCompile(`(?is)enter passphrase for(?: key)?\s+(?:'([^']*)'|(.*?))\s*:\s*$`)
	// A password prompt names the account it is for, which is how tram tells a
	// jump station's password from the destination's.
	passwordPrompt = regexp.MustCompile(`(?i)^(?:([^@\s]+)@)?([^@\s':]+)(?:'s)?\s+password`)
)

// Askpass answers one question from ssh and writes the answer to out.
//
// It answers exactly two kinds of question: a password for the identity being
// asked about, and a passphrase for a key file named in the prompt. Anything
// else is refused, and a host key confirmation is refused deliberately and
// permanently. Answering "yes" to an unknown fingerprint on the user's behalf
// would turn a warning about a possible interception into a silent accept.
//
// In learn mode, a question tram has no answer for is put to the user on the
// console and the reply is parked under the session's nonce. It is only written
// to the keyring for real once the session it was used for has succeeded, so a
// mistyped password is never remembered.
func Askpass(st *Store, prompt string, out io.Writer) error {
	if hostKeyPrompt.MatchString(prompt) {
		return fmt.Errorf("tram never answers host key questions; answer it yourself")
	}
	learn := os.Getenv(EnvLearn)

	if m := passphrasePrompt.FindStringSubmatch(strings.TrimSpace(prompt)); m != nil {
		key := m[1]
		if key == "" {
			key = m[2]
		}
		key = strings.TrimSpace(strings.Trim(key, `'"`))
		if key == "" {
			return fmt.Errorf("passphrase prompt did not name a key file")
		}
		sub := PassphraseFor(key)
		if v, err := st.Get(sub); err == nil {
			return write(out, v)
		}
		if learn == "" {
			return fmt.Errorf("no passphrase stored for %s", key)
		}
		return capture(st, out, learn, sub, "Passphrase for "+key+": ")
	}

	if !strings.Contains(strings.ToLower(prompt), "password") {
		return fmt.Errorf("unrecognised prompt: %s", prompt)
	}

	for _, sub := range passwordSubjects(prompt) {
		if v, err := st.Get(sub); err == nil {
			return write(out, v)
		}
	}
	if learn == "" {
		return fmt.Errorf("no password stored for %s", strings.TrimSpace(prompt))
	}
	return capture(st, out, learn, learnSubject(prompt), strings.TrimRight(prompt, " ")+" ")
}

// learnSubject decides what a newly typed password should be remembered as.
//
// For the destination it is whatever the session was armed with, which is the
// account when the host is linked to one. That is the difference between
// typing a password once for a fleet and typing it once per machine: a
// password authenticates an identity, not an address. For any other host in
// the prompt, meaning a jump station, it is that station's own subject.
func learnSubject(prompt string) Subject {
	host := promptHost(prompt)
	dest := os.Getenv(EnvHost)
	token := Subject(os.Getenv(EnvToken))

	if host == "" || (dest != "" && strings.EqualFold(host, dest)) {
		if token != "" {
			return token
		}
	}
	if host != "" {
		return PasswordFor("host", host)
	}
	if token != "" {
		return token
	}
	return PasswordFor("host", "unknown")
}

// promptHost reads the host name out of a password prompt, or "" when the
// wording does not carry one.
func promptHost(prompt string) string {
	if m := passwordPrompt.FindStringSubmatch(strings.TrimSpace(prompt)); m != nil {
		return m[2]
	}
	return ""
}

// passwordSubjects lists the subjects that could answer a password prompt, best
// guess first.
//
// The host named in the prompt comes first, because ssh asks for a jump
// station's password with the station's own name in it. The session's own
// identity is only offered when the prompt is about the destination, or when
// the prompt names nobody. Handing it to a machine on the way would give one
// host's password to another, silently, and the whole reason ssh prints the
// name in the prompt is so a person can decide that for themselves.
//
// The station is not left stuck: it gets asked for once, on its own account,
// and remembered under its own name.
func passwordSubjects(prompt string) []Subject {
	var out []Subject
	host := promptHost(prompt)
	if host != "" {
		out = append(out, PasswordFor("host", host))
	}

	dest := os.Getenv(EnvHost)
	forDestination := host == "" || dest == "" || strings.EqualFold(host, dest)
	if tok := os.Getenv(EnvToken); tok != "" && forDestination {
		s := Subject(tok)
		if len(out) == 0 || out[0] != s {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = append(out, PasswordFor("host", "unknown"))
	}
	return out
}

// capture asks the user, parks the answer under the session nonce, and hands it
// to ssh.
func capture(st *Store, out io.Writer, nonce string, sub Subject, prompt string) error {
	v, err := AskOnTTY(prompt)
	if err != nil {
		return err
	}
	if err := st.Set(pendingSubject(nonce), v); err != nil {
		// The session can still go ahead; only the remembering is lost.
		NoteOnTTY("tram could not park the secret to remember it: %v", err)
		return write(out, v)
	}
	if err := st.Remember(pendingSubject(nonce), "pending "+string(sub)); err != nil {
		NoteOnTTY("tram could not record what to remember: %v", err)
	}
	return write(out, v)
}

func write(out io.Writer, v string) error {
	_, err := io.WriteString(out, v+"\n")
	return err
}

// ---- the pending secret ---------------------------------------------------

// pendingSubject is where a captured secret waits while the session it was
// typed for runs.
func pendingSubject(nonce string) Subject { return Subject("pending:" + nonce) }

// NewNonce returns an identifier for one session's learn attempt.
func NewNonce() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(int64(os.Getpid()), 16)
	}
	return hex.EncodeToString(b)
}

// Learned describes a secret captured during a session that has not yet been
// committed.
type Learned struct {
	Subject Subject
	Value   string
}

// TakePending collects whatever the helper captured during a session and clears
// it, returning nothing when the helper was never asked.
func (s *Store) TakePending(nonce string) (Learned, bool) {
	pend := pendingSubject(nonce)
	v, err := s.Get(pend)
	where := s.Where(pend)
	_ = s.Delete(pend)
	_ = s.Forget(pend)
	if err != nil || v == "" {
		return Learned{}, false
	}
	sub := Subject(strings.TrimPrefix(where, "pending "))
	if sub == "" || !strings.Contains(string(sub), ":") {
		return Learned{}, false
	}
	return Learned{Subject: sub, Value: v}, true
}

// Commit writes a captured secret where it belongs, now that the session has
// shown it was the right one.
func (s *Store) Commit(l Learned) error {
	if err := s.Set(l.Subject, l.Value); err != nil {
		return err
	}
	return s.Remember(l.Subject, s.Backend())
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
// This decides whether stored passwords are worth anything at all. In tram's
// handoff mode ssh has a real terminal, and given the choice it prompts on that
// terminal and ignores the askpass helper entirely. Only SSH_ASKPASS_REQUIRE,
// added in OpenSSH 8.4, makes it use the helper anyway. On an older ssh the
// feature cannot work, and tram says so when you try to store a password
// instead of letting you find out at connection time.
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
