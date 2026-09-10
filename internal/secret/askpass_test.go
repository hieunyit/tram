package secret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets this test binary stand in for the tram binary when ssh calls
// an askpass helper. That is the only honest way to check the mechanism: what
// matters is not that tram's own code returns a string, but that ssh actually
// runs the helper and uses what it says.
func TestMain(m *testing.M) {
	if os.Getenv("TRAM_TEST_ASKPASS") != "" {
		prompt := ""
		if len(os.Args) > 1 {
			prompt = os.Args[1]
		}
		st := New(os.Getenv("TRAM_TEST_STORE"))
		if err := Askpass(st, prompt, os.Stdout); err != nil {
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
// key and checks that the key was loaded using a passphrase the helper served.
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

	// The passphrase goes in the store the helper will read, never on a
	// command line and never in an environment variable.
	storeDir := filepath.Join(dir, "state")
	st := New(storeDir)
	sub := PassphraseFor(keyPath)
	if err := st.Set(sub, passphrase); err != nil {
		t.Fatalf("store the passphrase: %v", err)
	}
	t.Cleanup(func() { _ = st.Delete(sub) })

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
		"TRAM_TEST_STORE="+storeDir,
	)
	// No standard input at all, exactly as in a script. If the helper is not
	// used, ssh-add has nowhere to ask and must fail.
	cmd.Stdin = nil
	addOut, addErr := cmd.CombinedOutput()
	if addErr != nil {
		t.Fatalf("ssh-add did not use the askpass helper: %v\n%s\n\n"+
			"This is the mechanism stored passwords depend on. Without it, tram cannot "+
			"answer ssh's prompts and the remembering feature is worthless on this platform.",
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

// TestAskpassRefusesWithoutAStoredSecret checks the other half: the helper does
// not invent an answer, so a wrong or missing passphrase fails loudly.
func TestAskpassRefusesWithoutAStoredSecret(t *testing.T) {
	keygen := requireTool(t, "ssh-keygen")
	agent := requireTool(t, "ssh-agent")
	requireTool(t, "ssh-add")

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-f", keyPath, "-N", "a passphrase nobody stored").CombinedOutput(); err != nil {
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
		"TRAM_TEST_STORE="+filepath.Join(dir, "empty"),
	)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("ssh-add succeeded with no stored passphrase:\n%s", out)
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
	st := New(t.TempDir())
	t.Setenv(EnvLearn, "nonce") // even in learn mode, where it could ask
	prompts := []string{
		"The authenticity of host 'x (1.2.3.4)' can't be established.\nED25519 key fingerprint is SHA256:abc.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@",
		"Host key verification failed.",
	}
	for _, p := range prompts {
		var sb strings.Builder
		if err := Askpass(st, p, &sb); err == nil {
			t.Errorf("answered a host key question with %q", sb.String())
		}
		if sb.Len() != 0 {
			t.Errorf("wrote %q to ssh for a host key question", sb.String())
		}
	}
}

// TestPasswordPromptNamesTheRightHost covers a jump chain: ssh asks for the
// station's password with the station's own name in the prompt, and handing it
// the destination's password would be both wrong and a leak.
func TestPasswordPromptNamesTheRightHost(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	if err := st.Set(PasswordFor("host", "bastion"), "bastion-secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(PasswordFor("host", "web1"), "web1-secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Delete(PasswordFor("host", "bastion"))
		_ = st.Delete(PasswordFor("host", "web1"))
	})

	// The session was opened for web1, so that is the token.
	t.Setenv(EnvToken, string(PasswordFor("host", "web1")))

	var sb strings.Builder
	if err := Askpass(st, "jump@bastion's password: ", &sb); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sb.String()); got != "bastion-secret" {
		t.Errorf("served %q for the bastion's prompt; the destination's password must not go to a jump station", got)
	}

	sb.Reset()
	if err := Askpass(st, "deploy@web1's password: ", &sb); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(sb.String()); got != "web1-secret" {
		t.Errorf("served %q for the destination", got)
	}
}

// TestPendingSecretIsOnlyKeptWhenCommitted covers the promise that a mistyped
// password is not remembered: the capture is parked, and a session that fails
// throws it away.
func TestPendingSecretIsOnlyKeptWhenCommitted(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	nonce := NewNonce()
	real := PasswordFor("host", "web9")

	if err := st.Set(pendingSubject(nonce), "typed-once"); err != nil {
		t.Fatal(err)
	}
	if err := st.Remember(pendingSubject(nonce), "pending "+string(real)); err != nil {
		t.Fatal(err)
	}

	l, ok := st.TakePending(nonce)
	if !ok {
		t.Fatal("nothing was parked")
	}
	if l.Value != "typed-once" || l.Subject != real {
		t.Fatalf("parked value came back as %+v", l)
	}
	// Taking it must clear it, so a second session cannot pick up the first
	// session's answer.
	if _, ok := st.TakePending(nonce); ok {
		t.Error("the parked secret survived being taken")
	}
	if _, err := st.Get(real); err == nil {
		t.Error("the secret was stored without being committed")
	}

	if err := st.Commit(l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Delete(real) })
	if v, err := st.Get(real); err != nil || v != "typed-once" {
		t.Errorf("commit did not store the secret: %q %v", v, err)
	}
}

// TestLearnModeIsOffWithoutTheNonce checks that tram does not start asking
// questions of its own accord.
func TestLearnModeIsOffWithoutTheNonce(t *testing.T) {
	st := New(t.TempDir())
	t.Setenv(EnvToken, string(PasswordFor("host", "web1")))
	os.Unsetenv(EnvLearn)

	var sb strings.Builder
	err := Askpass(st, "deploy@web1's password: ", &sb)
	if err == nil {
		t.Error("with nothing stored and no learn nonce, the helper should refuse rather than prompt")
	}
	if sb.Len() != 0 {
		t.Errorf("wrote %q to ssh", sb.String())
	}
}

// TestPassphrasePromptParsing locks down the wordings ssh uses to ask for a
// key's passphrase.
//
// The Windows case is the one that bit: an unquoted path carries a drive
// letter, so a pattern that simply reads up to a colon takes "C" and a pattern
// that reads everything takes the prompt's own trailing colon with it. Either
// way the stored passphrase is looked up under a path that does not exist.
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
		m := passphrasePrompt.FindStringSubmatch(strings.TrimSpace(c.prompt))
		if m == nil {
			t.Errorf("did not recognise %q as a passphrase prompt", c.prompt)
			continue
		}
		got := m[1]
		if got == "" {
			got = m[2]
		}
		if got != c.want {
			t.Errorf("from %q read key %q, want %q", c.prompt, got, c.want)
		}
	}
}

// TestPassphraseServedForAWindowsPath is the same bug seen from the outside:
// storing a passphrase and serving it back has to agree on the path.
func TestPassphraseServedForAWindowsPath(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	key := filepath.Join(dir, "id_test")
	if err := st.Set(PassphraseFor(key), "s3cret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Delete(PassphraseFor(key)) })

	var sb strings.Builder
	if err := Askpass(st, "Enter passphrase for "+key+": ", &sb); err != nil {
		t.Fatalf("stored under one path, looked up under another: %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "s3cret" {
		t.Errorf("served %q", got)
	}
}

// TestLearnedPasswordGoesToTheAccount covers the rule the plan states and the
// question it answers: open a second host that shares an identity and you are
// not asked again.
//
// A password authenticates a login, not an address. Remembering it per host
// would mean typing the same password once for every machine in a fleet, which
// is most of the way back to typing it every time.
func TestLearnedPasswordGoesToTheAccount(t *testing.T) {
	accountSubject := PasswordFor("account", "deploy")
	t.Setenv(EnvToken, string(accountSubject))
	t.Setenv(EnvHost, "web1")

	// The prompt names the destination, so the session's identity is used.
	if got := learnSubject("deploy@web1's password: "); got != accountSubject {
		t.Errorf("the destination's password would be remembered as %q, want the account", got)
	}

	// A prompt for something else on the way is that machine's own password.
	if got := learnSubject("jump@bastion's password: "); got != PasswordFor("host", "bastion") {
		t.Errorf("a jump station's password would be remembered as %q", got)
	}

	// With no account, the host itself is the only thing to key on.
	hostSubject := PasswordFor("host", "web1")
	t.Setenv(EnvToken, string(hostSubject))
	if got := learnSubject("deploy@web1's password: "); got != hostSubject {
		t.Errorf("an unlinked host would be remembered as %q", got)
	}
}

// TestAccountPasswordIsNotGivenToAMachineOnTheWay draws the line the serving
// side has to hold.
//
// One stored account password answers for the host the session was opened for,
// because that is the identity it belongs to. It is not handed to a jump
// station that happens to ask along the way: ssh prints the name in the prompt
// precisely so a person can decide, and answering silently for a different
// machine takes that decision away.
func TestAccountPasswordIsNotGivenToAMachineOnTheWay(t *testing.T) {
	st := New(t.TempDir())
	sub := PasswordFor("account", "deploy")
	if err := st.Set(sub, "fleet-wide"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Delete(sub) })
	t.Setenv(EnvToken, string(sub))
	t.Setenv(EnvHost, "web7")

	var sb strings.Builder
	if err := Askpass(st, "deploy@web7's password: ", &sb); err != nil {
		t.Fatalf("the destination was not answered: %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "fleet-wide" {
		t.Errorf("the destination was served %q", got)
	}

	sb.Reset()
	if err := Askpass(st, "root@bastion's password: ", &sb); err == nil {
		t.Errorf("a jump station was served the destination's password: %q", sb.String())
	}
	if sb.Len() != 0 {
		t.Errorf("wrote %q to ssh for a machine on the way", sb.String())
	}

	// A station with its own password still gets served, and is asked for only
	// once because it is remembered under its own name.
	bastion := PasswordFor("host", "bastion")
	if err := st.Set(bastion, "bastion-only"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Delete(bastion) })
	sb.Reset()
	if err := Askpass(st, "root@bastion's password: ", &sb); err != nil {
		t.Fatalf("a station with its own password was not answered: %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "bastion-only" {
		t.Errorf("the station was served %q", got)
	}
}

// TestPassphraseFoundWhateverTheKeyPathLooksLike covers a mismatch that would
// fail silently.
//
// A configuration says IdentityFile ~/.ssh/id_ed25519, so that is the spelling
// tram stores under. ssh asks for the passphrase using the path it resolved,
// absolute and with the platform's separators. If the two are not brought to
// the same form, the passphrase is in the keyring and is never found.
func TestPassphraseFoundWhateverTheKeyPathLooksLike(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	abs := filepath.Join(home, ".ssh", "id_tram_probe")
	spellings := []string{
		"~/.ssh/id_tram_probe",
		abs,
		filepath.ToSlash(abs),
		`"~/.ssh/id_tram_probe"`,
		strings.ToUpper(abs[:1]) + abs[1:],
	}

	want := PassphraseFor(spellings[0])
	for _, s := range spellings {
		if got := PassphraseFor(s); got != want {
			t.Errorf("%q maps to subject %q, want %q", s, got, want)
		}
	}

	st := New(t.TempDir())
	if err := st.Set(want, "opens-it"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Delete(want) })

	// ssh names the resolved path in its prompt, not the one from the file.
	var sb strings.Builder
	if err := Askpass(st, "Enter passphrase for "+abs+": ", &sb); err != nil {
		t.Fatalf("stored from the configuration's spelling, not found from ssh's: %v", err)
	}
	if got := strings.TrimSpace(sb.String()); got != "opens-it" {
		t.Errorf("served %q", got)
	}
}

// TestAskpassInvocationIsRecognisedWithoutTramsOwnEnvironment covers the case
// where SSH_ASKPASS points at tram but tram did not start the ssh.
//
// Without this, the prompt falls through to the command line and is read as a
// host name, and tram tries to open a session to "Enter passphrase for ...".
// It is worth supporting because pointing SSH_ASKPASS at tram permanently is a
// reasonable thing to do: every ssh then benefits, not only the ones tram ran.
func TestAskpassInvocationIsRecognisedWithoutTramsOwnEnvironment(t *testing.T) {
	os.Unsetenv(EnvToken)
	os.Unsetenv(EnvLearn)

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

	// Ordinary command lines must not be mistaken for prompts.
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
