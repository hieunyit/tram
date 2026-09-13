package tui

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hieuny/tram/internal/model"
)

// Unlocking before a batch.
//
// Measuring, diagnosing and running a command are runs nobody is sitting at:
// ssh is told to answer from this run's passphrases or not at all, so a key
// nobody has unlocked yet makes every host behind it fail. Failing that way
// looks exactly like a broken host. So the keys are asked about first, in
// tram's own box, once each, and only then does the batch start. A key can be
// skipped; its hosts then come back LOCKED rather than AUTH, which says what is
// actually in the way.

// lockChecks is how many hosts are asked about at once. Each check asks ssh
// which keys a host would offer, which is a process, and a sweep over a whole
// fleet one at a time is seconds of a frozen status line.
const lockChecks = 8

// unlockPlan is one batch waiting on its keys.
type unlockPlan struct {
	what    string
	hosts   []model.Host
	skipped map[string]bool
	then    func() (tea.Model, tea.Cmd)
}

// lockedKey is one key file and the hosts it stands in front of.
type lockedKey struct {
	path  string
	hosts []string
}

// lockedMsg reports which keys a batch is waiting on.
type lockedMsg struct {
	plan *unlockPlan
	keys []lockedKey
}

// unlockThen asks for every locked key in front of hosts, then runs then.
func (m *Model) unlockThen(what string, hosts []model.Host, then func() (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	if m.Runner == nil || len(hosts) == 0 {
		return then()
	}
	plan := &unlockPlan{
		what:    what,
		hosts:   append([]model.Host(nil), hosts...),
		skipped: map[string]bool{},
		then:    then,
	}
	return m, m.checkLocks(plan)
}

// checkLocks looks for locked keys off the main loop.
func (m *Model) checkLocks(plan *unlockPlan) tea.Cmd {
	m.status = fmt.Sprintf("checking keys for %d host(s)", len(plan.hosts))
	runner := m.Runner
	return func() tea.Msg {
		return lockedMsg{plan: plan, keys: lockedKeys(runner, plan.hosts, plan.skipped)}
	}
}

// lockedKeys groups hosts by the locked key in front of them, in the order the
// hosts came, leaving out keys already skipped.
func lockedKeys(r Runner, hosts []model.Host, skipped map[string]bool) []lockedKey {
	found := make([]string, len(hosts))
	sem := make(chan struct{}, lockChecks)
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			found[i] = r.Locked(h)
		}()
	}
	wg.Wait()

	var out []lockedKey
	index := map[string]int{}
	for i, key := range found {
		if key == "" || skipped[key] {
			continue
		}
		j, seen := index[key]
		if !seen {
			j = len(out)
			index[key] = j
			out = append(out, lockedKey{path: key})
		}
		out[j].hosts = append(out[j].hosts, hosts[i].Name)
	}
	return out
}

// askLocked puts the next key in the box, and starts the batch when none is
// left.
//
// Hosts behind a key just unlocked are looked at once more before starting,
// because the check names only the first locked key on a route: a jump station
// with a key of its own shows up only once the destination's is out of the way.
// Each round either unlocks a key it had not seen or ends, so it cannot go
// round for ever.
func (m *Model) askLocked(plan *unlockPlan, queue []lockedKey, unlocked []string) (tea.Model, tea.Cmd) {
	if len(queue) == 0 {
		m.status = ""
		if len(unlocked) == 0 {
			return plan.then()
		}
		plan.hosts = hostsNamed(plan.hosts, unlocked)
		return m, m.checkLocks(plan)
	}
	key, rest := queue[0], queue[1:]
	h, _ := hostNamed(plan.hosts, key.hosts[0])

	m.openPassphraseForm(h, key.path,
		func() (tea.Model, tea.Cmd) {
			return m.askLocked(plan, rest, append(unlocked, key.hosts...))
		},
		func() (tea.Model, tea.Cmd) {
			plan.skipped[key.path] = true
			return m.askLocked(plan, rest, unlocked)
		})
	m.form.title = "unlock a key before " + plan.what
	m.form.note = shortenPath(key.path) + "   " + m.gl.dot + "   " + usedBy(key.hosts) +
		"   " + m.gl.dot + "   esc skips it"
	return m, nil
}

// usedBy names the hosts behind a key without letting a long list take the box.
func usedBy(hosts []string) string {
	const shown = 3
	if len(hosts) <= shown {
		return "for " + strings.Join(hosts, ", ")
	}
	return fmt.Sprintf("for %s and %d more", strings.Join(hosts[:shown], ", "), len(hosts)-shown)
}

func hostNamed(hosts []model.Host, name string) (model.Host, bool) {
	for _, h := range hosts {
		if h.Name == name {
			return h, true
		}
	}
	return model.Host{Name: name}, false
}

func hostsNamed(hosts []model.Host, names []string) []model.Host {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []model.Host
	for _, h := range hosts {
		if want[h.Name] {
			out = append(out, h)
		}
	}
	return out
}
