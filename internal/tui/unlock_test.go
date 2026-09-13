package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/store"
)

// lockRunner has two keys, one for the jump station and one for everything
// else, and remembers what it was asked to measure.
type lockRunner struct {
	nullRunner
	mu       sync.Mutex
	unlocked map[string]bool
	measured []string
}

func (r *lockRunner) keyFor(h model.Host) string {
	if h.Name == "bastion" {
		return "/keys/station"
	}
	return "/keys/fleet"
}

func (r *lockRunner) Locked(h model.Host) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k := r.keyFor(h); !r.unlocked[k] {
		return k
	}
	return ""
}

func (r *lockRunner) Unlock(keyPath, passphrase string) error {
	if passphrase != "open" {
		return fmt.Errorf("that passphrase does not open %s", keyPath)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unlocked[keyPath] = true
	return nil
}

func (r *lockRunner) Measure(hosts []model.Host) []Measurement {
	for _, h := range hosts {
		r.measured = append(r.measured, h.Name)
	}
	return r.nullRunner.Measure(hosts)
}

// TestMeasuringAsksForEachLockedKeyOnceFirst is the reason for the step.
//
// A measurement is a run nobody is sitting at, so ssh is given the passphrases
// this run knows and nothing else. Without asking first, a key nobody had
// unlocked made every host behind it come back AUTH, which reads as a broken
// fleet. Each key is asked about once however many hosts share it, a key can
// be skipped, and the sweep starts either way.
func TestMeasuringAsksForEachLockedKeyOnceFirst(t *testing.T) {
	m := wide(t)
	r := &lockRunner{unlocked: map[string]bool{}}
	m.Runner = r

	_, cmd := m.Update(key("P"))
	drain(m, cmd)

	if len(r.measured) != 0 {
		t.Fatal("the sweep started before the keys were asked about")
	}
	if m.mode != modeForm || m.form.keyPath != "/keys/station" {
		t.Fatalf("expected the station's key first; mode %v, form %+v", m.mode, m.form)
	}
	if out := m.View(); !strings.Contains(out, "esc skips it") {
		t.Errorf("the box does not say escape skips the key:\n%s", out)
	}

	m.form.fields[0].input.SetValue("open")
	_, cmd = m.Update(key("ctrl+s"))
	drain(m, cmd)

	if m.mode != modeForm || m.form.keyPath != "/keys/fleet" {
		t.Fatalf("expected the fleet key next; mode %v", m.mode)
	}
	if !strings.Contains(m.form.note, "more") && !strings.Contains(m.form.note, "for ") {
		t.Errorf("the box does not say which hosts the key is for: %q", m.form.note)
	}

	_, cmd = m.Update(key("esc"))
	drain(m, cmd)

	if m.mode == modeForm {
		t.Fatal("a skipped key was asked about again")
	}
	if len(r.measured) != len(m.filtered) {
		t.Fatalf("measured %d host(s), want all %d", len(r.measured), len(m.filtered))
	}
	if !r.unlocked["/keys/station"] || r.unlocked["/keys/fleet"] {
		t.Errorf("unlocked = %v, want only the station's key", r.unlocked)
	}
}

// TestNothingLockedMeasuresStraightAway keeps the step out of the way when it
// has nothing to ask.
func TestNothingLockedMeasuresStraightAway(t *testing.T) {
	m := wide(t)
	r := &lockRunner{unlocked: map[string]bool{"/keys/station": true, "/keys/fleet": true}}
	m.Runner = r

	_, cmd := m.Update(key("p"))
	drain(m, cmd)

	if m.mode == modeForm {
		t.Fatal("asked for a passphrase with every key already unlocked")
	}
	if len(r.measured) == 0 {
		t.Fatal("nothing was measured")
	}
}

func TestLockedHealthIsNotPaintedAsDown(t *testing.T) {
	if got := healthOf(factWith("LOCKED")); got != healthLocked {
		t.Errorf("LOCKED reads as %v, want healthLocked", got)
	}
}

func factWith(class string) store.Fact { return store.Fact{Class: class, At: 1} }
