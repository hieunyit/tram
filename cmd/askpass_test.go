package cmd

import "testing"

// TestArmAskpassOnlyWithAnAnswerToGive is the regression that produced three
// separate complaints from one mistake.
//
// The helper exists to hand ssh a passphrase tram already has. Arming it
// without one does not make the question go away: it moves the question from
// ssh's own prompt to a child process borrowing the terminal in the middle of
// an authentication, which is a worse place to be asked and, when the interface
// is on screen, no place at all.
//
// The mistake was to fold "does this host have an encrypted key" together with
// "is its passphrase already known". Folded, a host stopped counting as having
// an encrypted key the moment its passphrase was cached, so the helper was
// disarmed exactly when it had something to offer, and every host after the
// first asked again.
func TestArmAskpassOnlyWithAnAnswerToGive(t *testing.T) {
	const (
		reuse   = true
		hasKey  = true
		cached  = true
		force   = true
		console = true
	)

	cases := []struct {
		name                                    string
		reuse, hasKey, cached, force, isConsole bool
		want                                    bool
	}{
		{
			name:  "an answer to give, and somewhere to give it",
			reuse: reuse, hasKey: hasKey, cached: cached, force: force, isConsole: console,
			want: true,
		},
		{
			name:  "no answer yet, so ssh asks in its own way",
			reuse: reuse, hasKey: hasKey, cached: false, force: force, isConsole: console,
			want: false,
		},
		{
			name:  "no encrypted key, so there is nothing to reuse",
			reuse: reuse, hasKey: false, cached: cached, force: force, isConsole: console,
			want: false,
		},
		{
			name:  "an ssh too old to be told to use a helper",
			reuse: reuse, hasKey: hasKey, cached: cached, force: false, isConsole: console,
			want: false,
		},
		{
			name:  "nowhere to relay a host key question to",
			reuse: reuse, hasKey: hasKey, cached: cached, force: force, isConsole: false,
			want: false,
		},
		{
			name:  "turned off in config.toml",
			reuse: false, hasKey: hasKey, cached: cached, force: force, isConsole: console,
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := armAskpass(c.reuse, c.hasKey, c.cached, c.force, c.isConsole)
			if got != c.want {
				t.Errorf("armAskpass = %v, want %v", got, c.want)
			}
		})
	}
}
