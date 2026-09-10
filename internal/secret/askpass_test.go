package secret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets this test binary stand in for the tram binary when ssh calls an
// askpass helper. That is the only honest way to check the mechanism: what
// matters is not that tram's own code returns a string, but that ssh actually
// runs the helper and uses what it says.
func TestMain(m *testing.M) {
	if os.Getenv("TRAM_TEST_ASKPASS") != "" {
		prompt := ""
		if len(os.Args) > 1 {
			prompt = os.Args[1]
		}
		// A test cannot type at a console, so the prompting path is switched off
		// here and only the serving path runs. What is under test is whether ssh
		// calls the helper at all and believes what it says.
		if os.Getenv("TRAM_TEST_NO_PROMPT") != "" {
			key, ok := PassphrasePath(prompt)
			if !ok {
				os.Exit(1)
			}
			v, cached := sessionFromEnv().Get(key)
			if !cached {
				os.Stderr.WriteString("askpass: nothing cached for " + key + "\n")
				os.Exit(1)
			}
			os.Stdout.WriteString(v + "\n")
			os.Exit(0)
		}
		if err := Askpass(prompt, os.Stdout); err != nil {
			os.Stderr.WriteString("askpass: " + err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func requireTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH", name)
	}
	return p
}

// TestAskpassIsActuallyUsedBySSH is the check the plan calls the project's
// highest risk.
//
// In tram's handoff mode ssh has a real terminal, and given the choice it asks
// on that terminal and ignores SSH_ASKPASS entirely. Only SSH_ASKPASS_REQUIRE,
// added in OpenSSH 8.4, makes it use the helper anyway. Reading the version
// number is not proof, so this runs the real ssh-add against a real encrypted
// key and checks that the key was loaded using a passphrase the helper served
// out of a session cache.
func TestAskpassIsActuallyUsedBySSH(t *testing.T) {
	keygen := requireTool(t, "ssh-keygen")
	agent := requireTool(t, "ssh-agent")
	requireTool(t, "ssh-add")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	const passphrase = "correct horse battery staple"

	out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-f", keyPath, "-N", passphrase, "-C", "tram-test").CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	sess, err := OpenSession(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Put(keyPath, passphrase); err != nil {
		t.Fatal(err)
	}

	sock, stop := startAgent(t, agent, dir)
	defer stop()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ssh-add", keyPath)
	cmd.Env = append(os.Environ(),
		"SSH_AUTH_SOCK="+sock,
		"SSH_ASKPASS="+self,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=tram",
		"TRAM_TEST_ASKPASS=1",
		"TRAM_TEST_NO_PROMPT=1",
	)
	cmd.Env = append(cmd.Env, sess.Env()...)
	// No standard input at all, exactly as in a script. If the helper is not
	// used, ssh-add has nowhere to ask and must fail.
	cmd.Stdin = nil
	addOut, addErr := cmd.CombinedOutput()
	if addErr != nil {
		t.Fatalf("ssh-add did not use the askpass helper: %v\n%s\n\n"+
			"This is the mechanism reusing a passphrase depends on. Without it tram "+
			"cannot answer ssh's prompts and every host asks again.",
			addErr, addOut)
	}

	list := exec.Command("ssh-add", "-l")
	list.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	listOut, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-add -l: %v\n%s", err, listOut)
	}
	if !strings.Contains(string(listOut), "tram-test") {
		t.Errorf("the agent does not hold the key:\n%s", listOut)
	}
}

// TestAskpassFailsWhenNothingIsCachedAndNobodyCanBeAsked checks the other half:
// the helper does not invent an answer.
func TestAskpassFailsWhenNothingIsCachedAndNobodyCanBeAsked(t *testing.T) {
	keygen := requireTool(t, "ssh-keygen")
	agent := requireTool(t, "ssh-agent")
	requireTool(t, "ssh-add")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-f", keyPath, "-N", "a passphrase nobody cached").CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	sock, stop := startAgent(t, agent, dir)
	defer stop()

	self, _ := os.Executable()
	cmd := exec.Command("ssh-add", keyPath)
	cmd.Env = append(os.Environ(),
		"SSH_AUTH_SOCK="+sock,
		"SSH_ASKPASS="+self,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=tram",
		"TRAM_TEST_ASKPASS=1",
		"TRAM_TEST_NO_PROMPT=1",
	)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("ssh-add succeeded with nothing cached and no console:\n%s", out)
	}
}

// startAgent runs an ssh-agent and returns the socket to talk to it.
func startAgent(t *testing.T, agent, dir string) (string, func()) {
	t.Helper()
	out, err := exec.Command(agent, "-s").Output()
	if err != nil {
		t.Skipf("could not start ssh-agent: %v", err)
	}

	var sock, pid string
	for _, part := range strings.Split(string(out), ";") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "SSH_AUTH_SOCK="); ok {
			sock = v
		}
		if v, ok := strings.CutPrefix(part, "SSH_AGENT_PID="); ok {
			pid = v
		}
	}
	if sock == "" {
		t.Skipf("ssh-agent did not report a socket: %s", out)
	}
	return sock, func() {
		if pid != "" {
			kill := exec.Command(agent, "-k")
			kill.Env = append(os.Environ(), "SSH_AGENT_PID="+pid, "SSH_AUTH_SOCK="+sock)
			_ = kill.Run()
		}
	}
}

// TestAskpassNeverAnswersHostKeyQuestions is a rule rather than a behaviour:
// answering "yes" to an unrecognised fingerprint on the user's behalf would
// turn a warning about a possible interception into a silent accept.
func TestAskpassNeverAnswersHostKeyQuestions(t *testing.T) {
	prompts := []string{
		"The authenticity of host 'x (1.2.3.4)' can't be established.\nED25519 key fingerprint is SHA256:abc.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@",
		"Host key verification failed.",
	}
	for _, p := range prompts {
		var sb strings.Builder
		if err := Askpass(p, &sb); err == nil {
			t.Errorf("answered a host key question with %q", sb.String())
		}
		if sb.Len() != 0 {
			t.Errorf("wrote %q to ssh for a host key question", sb.String())
		}
	}
}

// TestPassphrasePromptParsing locks down the wordings ssh uses to ask for a
// key's passphrase.
//
// The Windows case is the one that bit: an unquoted path carries a drive
// letter, so a pattern that reads up to a colon takes "C" and one that reads
// everything takes the prompt's own trailing colon with it. Either way the
// cached passphrase is looked up under a path that does not exist.
func TestPassphrasePromptParsing(t *testing.T) {
	cases := []struct {
		prompt string
		want   string
	}{
		{"Enter passphrase for key '/home/me/.ssh/id_ed25519': ", "/home/me/.ssh/id_ed25519"},
		{`Enter passphrase for C:\Users\me\.ssh\id_test: `, `C:\Users\me\.ssh\id_test`},
		{"Enter passphrase for /home/me/.ssh/id_rsa:", "/home/me/.ssh/id_rsa"},
		{"Enter passphrase for key '/path/with space/id': ", "/path/with space/id"},
	}
	for _, c := range cases {
		got, ok := PassphrasePath(c.prompt)
		if !ok {
			t.Errorf("did not recognise %q as a passphrase prompt", c.prompt)
			continue
		}
		if got != c.want {
			t.Errorf("from %q read key %q, want %q", c.prompt, got, c.want)
		}
	}
	if _, ok := PassphrasePath("deploy@web1's password: "); ok {
		t.Error("a password prompt was read as a passphrase prompt")
	}
}

// TestAskpassInvocationIsRecognisedByShape covers the case where SSH_ASKPASS
// points at tram but tram did not start the ssh.
//
// Without this, the prompt falls through to the command line and is read as a
// host name, and tram tries to open a session to "Enter passphrase for ...".
func TestAskpassInvocationIsRecognisedByShape(t *testing.T) {
	prompts := []string{
		`Enter passphrase for C:\Users\me\.ssh\id_ed25519: `,
		"Enter passphrase for key '/home/me/.ssh/id_rsa': ",
		"deploy@web1's password: ",
		"root@10.0.0.1's password: ",
		"The authenticity of host 'web1 (10.0.0.1)' can't be established. Are you sure you want to continue connecting (yes/no)? ",
	}
	for _, p := range prompts {
		if !IsAskpassInvocation([]string{p}) {
			t.Errorf("not recognised as a prompt: %q", p)
		}
	}

	commands := [][]string{
		{"ls"}, {"web1"}, {"ls", "--wide"}, {"import", "inventory.ini"},
		{"add", "web2", "--addr", "10.0.0.2"}, {}, {"my-host.example.com"},
	}
	for _, c := range commands {
		if IsAskpassInvocation(c) {
			t.Errorf("%v was mistaken for an askpass prompt", c)
		}
	}
}
