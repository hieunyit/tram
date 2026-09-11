package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hieuny/tram/internal/model"
	"github.com/hieuny/tram/internal/store"
)

// This file holds what the interface knows about the state of the machines, as
// opposed to what the configuration file says about them.
//
// Everything here is measured, and everything measured can be missing. A host
// nobody has probed has no latency, no load and no operating system, and the
// interface says so with a dash rather than inventing a zero.

// Measurement is what one probe learned about one host. It is the shape the
// command layer hands back, so that the interface never runs ssh itself.
type Measurement struct {
	Host string
	// Class is probe's own spelling: OK, TIMEOUT, AUTH and so on.
	Class  string
	OK     bool
	Millis int64
	// The three facts only a command on the machine can answer. They stay empty
	// when the host answered but had nothing to run, which is what a network
	// device does.
	OS     string
	Load   string
	Uptime string
	Detail string
}

// tab is which of the three views is showing. The design has a fourth, tunnels,
// which tram does not do: v1 leaves port forwarding to ssh, so drawing a tab
// for it would be drawing a promise.
type tab int

const (
	tabHosts tab = iota
	tabSessions
	tabKeys
)

var allTabs = []tab{tabHosts, tabSessions, tabKeys}

func (t tab) title() string {
	switch t {
	case tabSessions:
		return "SESSIONS"
	case tabKeys:
		return "KEYS"
	}
	return "HOSTS"
}

// sortKey is the column the table is ordered by.
type sortKey int

const (
	sortAlias sortKey = iota
	sortHost
	sortLatency
	sortSeen
)

func (k sortKey) String() string {
	switch k {
	case sortHost:
		return "user@host"
	case sortLatency:
		return "latency"
	case sortSeen:
		return "last seen"
	}
	return "alias"
}

// fact returns what has been measured about a host.
func (m *Model) fact(host string) store.Fact { return m.inv.Store.Fact(host) }

// health names a host's state the way the design colours it.
type health int

const (
	healthUnknown health = iota
	healthUp
	healthSlow
	healthDown
)

func healthOf(f store.Fact) health {
	switch {
	case !f.Measured():
		return healthUnknown
	case !f.Reachable():
		return healthDown
	case f.Slow():
		return healthSlow
	}
	return healthUp
}

// ---- the agent ------------------------------------------------------------

// agentMsg carries what ssh-add had to say.
type agentMsg string

// checkAgent asks once, at startup. It is a command rather than a call because
// ssh-add talks to a socket, and a socket that nobody is listening on can take
// a moment to say so.
func (m *Model) checkAgent() tea.Cmd {
	if m.Runner == nil {
		return nil
	}
	runner := m.Runner
	return func() tea.Msg { return agentMsg(runner.Agent()) }
}

// ---- measuring ------------------------------------------------------------

// measuredMsg reports that a sweep finished.
type measuredMsg struct {
	count int
	up    int
	down  int
}

// measure probes hosts and records what comes back.
//
// It runs as a command rather than inline: a sweep across a real fleet is
// hundreds of ssh connections, and the interface has to stay usable while they
// happen. The results land as a message, which is the only place the model is
// changed.
func (m *Model) measure(hosts []model.Host) tea.Cmd {
	if len(hosts) == 0 || m.Runner == nil {
		return nil
	}
	runner := m.Runner
	st := m.inv.Store
	return func() tea.Msg {
		out := runner.Measure(hosts)
		facts := make(map[string]store.Fact, len(out))
		up, down := 0, 0
		for _, x := range out {
			facts[x.Host] = store.Fact{
				Class:  x.Class,
				Millis: x.Millis,
				OS:     x.OS,
				Load:   x.Load,
				Uptime: x.Uptime,
			}
			if x.OK {
				up++
			} else {
				down++
			}
		}
		if err := st.PutFacts(facts); err != nil {
			return errMsg{err}
		}
		return measuredMsg{count: len(out), up: up, down: down}
	}
}

// fleetLine is the count under the health sparkline.
func (m *Model) fleetLine() string {
	up, slow, down, unknown := m.inv.Store.FleetHealth(model.Names(m.hosts))
	parts := []string{
		m.st.ok.Render(fmt.Sprintf("%d", up)) + m.st.faint.Render(" up"),
		m.st.warn.Render(fmt.Sprintf("%d", slow)) + m.st.faint.Render(" slow"),
		m.st.bad.Render(fmt.Sprintf("%d", down)) + m.st.faint.Render(" down"),
	}
	if unknown > 0 {
		parts = append(parts, m.st.faint.Render(fmt.Sprintf("%d ?", unknown)))
	}
	return strings.Join(parts, "  ")
}

// fleetBars draws one bar per sweep, tall when everything answered.
func (m *Model) fleetBars(width int) string {
	sweeps := m.inv.Store.Sweeps()
	if len(sweeps) == 0 {
		return ""
	}
	var vals []float64
	var cols []lipgloss.Style
	for _, s := range sweeps {
		total := s.Up + s.Slow + s.Down
		if total == 0 {
			continue
		}
		good := float64(s.Up+s.Slow) / float64(total)
		vals = append(vals, 0.2+0.8*good)
		switch {
		case s.Down > 0 && float64(s.Down)/float64(total) > 0.1:
			cols = append(cols, m.st.bad)
		case s.Slow > 0:
			cols = append(cols, m.st.warn)
		default:
			cols = append(cols, m.st.ok)
		}
	}
	return m.sparkline(vals, cols, width)
}
