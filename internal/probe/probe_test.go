package probe

import "testing"

// TestClassify uses the wordings OpenSSH actually produces. Each case is a
// failure a user would go and fix somewhere different, which is the whole
// reason tram classifies instead of reporting "connection failed".
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want Class
	}{
		{"refused", "ssh: connect to host 10.0.0.1 port 22: Connection refused", Refused},
		{"timeout", "ssh: connect to host 10.0.0.1 port 22: Connection timed out", Timeout},
		{"dns", "ssh: Could not resolve hostname nope.invalid: Name or service not known", DNS},
		{"dns other wording", "ssh: Could not resolve hostname x: nodename nor servname provided", DNS},
		{"auth", "deploy@10.0.0.1: Permission denied (publickey,password).", Auth},
		{"auth too many", "Received disconnect from 10.0.0.1: Too many authentication failures", Auth},
		{"host key changed", "@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@@", HostKey},
		{"host key unverified", "Host key verification failed.", HostKey},
		{"unreachable", "ssh: connect to host 10.0.0.1 port 22: Network is unreachable", Timeout},
		{"bad option", "/home/me/.ssh/config: line 4: Bad configuration option: proxyjumpp", Config},
		{"kex", "kex_exchange_identification: Connection closed by remote host", Refused},
		{"nothing recognisable", "something nobody has seen before", Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, detail := Classify(c.out, 255)
			if got != c.want {
				t.Errorf("Classify(%q) = %s, want %s", c.out, got, c.want)
			}
			if detail == "" {
				t.Error("the detail is empty; a class must always be traceable to a line ssh wrote")
			}
		})
	}
}

// TestClassifyPrefersTheSpecificLine checks that the line that explains the
// failure wins over the noise around it, whichever order they arrive in.
func TestClassifyPrefersTheSpecificLine(t *testing.T) {
	out := "ssh: connect to host bastion port 22: Connection refused\r\n" +
		"ssh_exchange_identification: Connection closed by remote host\r\n"
	got, detail := Classify(out, 255)
	if got != Refused {
		t.Errorf("class = %s, want REFUSED", got)
	}
	if detail != "ssh: connect to host bastion port 22: Connection refused" {
		t.Errorf("detail = %q", detail)
	}
}

func TestClassifySuccess(t *testing.T) {
	if got, _ := Classify("", 0); got != OK {
		t.Errorf("exit 0 classified as %s", got)
	}
}

// TestHopOfIgnoresTheDestinationsOwnAddress is the fix for a mistake that is
// easy to make: ssh names the address it dialled, so a refused connection to a
// host with a HostName reads like a jump station failing unless the
// destination's address is recognised too.
func TestHopOfIgnoresTheDestinationsOwnAddress(t *testing.T) {
	line := "ssh: connect to host 127.0.0.1 port 2: Connection refused"
	if hop := HopOf(line, "localnope", "127.0.0.1"); hop != "" {
		t.Errorf("the destination was reported as a jump station: %q", hop)
	}
	if hop := HopOf(line, "localnope", "10.0.0.5"); hop != "127.0.0.1" {
		t.Errorf("a real jump station was not named: %q", hop)
	}
	if hop := HopOf("deploy@web1: Permission denied (publickey).", "web1", "10.0.0.1"); hop != "" {
		t.Errorf("an auth failure was read as a jump: %q", hop)
	}
}

func TestEveryClassExplainsItself(t *testing.T) {
	for _, c := range []Class{OK, Auth, Refused, Timeout, DNS, HostKey, Jump, Config, Unknown} {
		if c.Explain() == "" {
			t.Errorf("class %s has no explanation", c)
		}
	}
}

// TestBatchModeGivesWayToTheHelper pins down why the two cannot go together.
// BatchMode stops ssh consulting the helper at all, so a passphrase already
// given in this run could not be used; and it never reached the jump stations
// anyway. With the helper armed, it is the helper that keeps the run silent.
func TestBatchModeGivesWayToTheHelper(t *testing.T) {
	has := func(args []string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" && args[i+1] == "BatchMode=yes" {
				return true
			}
		}
		return false
	}
	if !has(Args("web", Options{})) {
		t.Error("without a helper the probe must run in BatchMode")
	}
	if has(Args("web", Options{Helper: true})) {
		t.Error("with a helper armed, BatchMode would stop ssh from using it")
	}
}
