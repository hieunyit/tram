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

// EnvToken names the subject the askpass helper should answer for. It holds a
// subject name, not a secret, so it is safe in the child's environment.
const EnvToken = "TRAM_ASKPASS_TOKEN"

// IsAskpassInvocation reports whether this process was started by ssh asking a
// question rather than by a user running a command.
func IsAskpassInvocation() bool { return os.Getenv(EnvToken) != "" }

var (
	// ssh asks about a host key in several wordings across versions, but every
	// one of them offers a yes/no choice about trusting a fingerprint.
	hostKeyPrompt = regexp.MustCompile(`(?i)authenticity of host|continue connecting|fingerprint|host key.*(changed|verification)`)
	// A passphrase prompt names the key file, which is what tells tram which
	// stored passphrase to hand back.
	passphrasePrompt = regexp.MustCompile(`(?i)enter passphrase for( key)?\s*'?([^']*)'?`)
)

// Askpass answers one question from ssh and writes the answer to out.
//
// It answers exactly two kinds of question: a password for the identity named
// by the token, and a passphrase for a key file named in the prompt. Anything
// else is refused, and a host key confirmation is refused deliberately and
// permanently. Answering "yes" to an unknown fingerprint on the user's behalf
// would turn a warning about a possible interception into a silent accept, so
// tram declines and lets ssh ask the human.
func Askpass(st *Store, prompt string, out io.Writer) error {
	if hostKeyPrompt.MatchString(prompt) {
		return fmt.Errorf("tram never answers host key questions; answer it yourself")
	}

	if m := passphrasePrompt.FindStringSubmatch(prompt); m != nil {
		key := strings.TrimSpace(strings.Trim(m[2], `'"`))
		if key == "" {
			return fmt.Errorf("passphrase prompt did not name a key file")
		}
		v, err := st.Get(PassphraseFor(key))
		if err != nil {
			return fmt.Errorf("no passphrase stored for %s", key)
		}
		_, err = io.WriteString(out, v+"\n")
		return err
	}

	if !strings.Contains(strings.ToLower(prompt), "password") {
		return fmt.Errorf("unrecognised prompt: %s", prompt)
	}
	sub := Subject(os.Getenv(EnvToken))
	if sub == "" {
		return fmt.Errorf("no subject given")
	}
	v, err := st.Get(sub)
	if err != nil {
		return fmt.Errorf("no password stored for %s", sub.Label())
	}
	_, err = io.WriteString(out, v+"\n")
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
