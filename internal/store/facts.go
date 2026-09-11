package store

import (
	"sort"
	"strings"
	"time"
)

// This file holds what tram has measured, as opposed to what the user wrote.
//
// Nothing in here is authoritative: it is a cache of observations, and every
// reader must cope with it being empty. A fresh install has measured nothing,
// and the interface says so rather than drawing a zero as though it were a
// reading.

// SlowMillis is the round trip above which a host is drawn as slow rather than
// up. It is a display threshold, not a judgement about the host.
const SlowMillis = 100

// samplesKept and eventsKept bound the two per-host lists. They are small on
// purpose: this is a sparkline and a recent-activity list, not a monitoring
// system, and an unbounded file would grow without anyone asking it to.
const (
	samplesKept = 30
	eventsKept  = 6
	sweepsKept  = 24
)

// Fact is the last thing tram learned about a host.
type Fact struct {
	// Class is the probe class of the last measurement: OK, TIMEOUT, AUTH and
	// so on, in probe's own spelling.
	Class  string `json:"class,omitempty"`
	Millis int64  `json:"ms,omitempty"`
	At     int64  `json:"at,omitempty"`

	// Detail is the line ssh itself wrote when the probe failed, and Explain
	// what that class of failure means. Both are kept so that the interface can
	// say what went wrong without probing the host again to find out.
	Detail  string `json:"detail,omitempty"`
	Explain string `json:"explain,omitempty"`

	// The facts that only a command on the machine itself can answer. They are
	// empty until something has run there.
	OS      string `json:"os,omitempty"`
	Load    string `json:"load,omitempty"`
	Uptime  string `json:"uptime,omitempty"`
	Disk    string `json:"disk,omitempty"`
	RAM     string `json:"ram,omitempty"`
	FactsAt int64  `json:"facts_at,omitempty"`

	// Samples are the last measurements, oldest first, which is what the
	// reachability sparkline draws.
	Samples []Sample `json:"samples,omitempty"`
}

// Sample is one measurement of one host.
type Sample struct {
	At     int64 `json:"at"`
	Millis int64 `json:"ms"`
	OK     bool  `json:"ok"`
}

// Event is something that happened to a host, for the recent list.
type Event struct {
	At   int64  `json:"at"`
	What string `json:"what"`
}

// Sweep is the outcome of one run across the fleet, for the health sparkline.
type Sweep struct {
	At   int64 `json:"at"`
	Up   int   `json:"up"`
	Slow int   `json:"slow"`
	Down int   `json:"down"`
}

type factsFile struct {
	Hosts  map[string]Fact    `json:"hosts"`
	Events map[string][]Event `json:"events"`
	Sweeps []Sweep            `json:"sweeps"`
}

// Reachable reports whether the last measurement got through.
func (f Fact) Reachable() bool { return f.Class == "OK" }

// Full reports whether a percentage reading has passed a threshold worth
// colouring. It takes the readings as they are written, "83%", because that is
// how the machine said them.
func Full(pct string, at int) bool {
	n := 0
	for _, r := range pct {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n >= at
}

// Slow reports whether the host answered, but not quickly.
func (f Fact) Slow() bool { return f.Reachable() && f.Millis >= SlowMillis }

// Measured reports whether anything has been measured at all, which is the
// question every reader has to ask before drawing a number.
func (f Fact) Measured() bool { return f.At > 0 }

// Uptime24h is the share of kept samples that got through, and how many samples
// that share is drawn from. A caller showing a percentage over two samples
// should say so rather than present it as a day's worth of evidence.
func (f Fact) Uptime24h() (pct int, samples int) {
	if len(f.Samples) == 0 {
		return 0, 0
	}
	ok := 0
	for _, s := range f.Samples {
		if s.OK {
			ok++
		}
	}
	return ok * 100 / len(f.Samples), len(f.Samples)
}

// Fact returns what is known about a host.
func (s *Store) Fact(host string) Fact {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Facts.Hosts[strings.ToLower(host)]
}

// PutFacts records a whole sweep at once and saves the file one time.
//
// Measuring a fleet touches hundreds of hosts, and writing the file once per
// host would be hundreds of rewrites of the same file for one user action.
func (s *Store) PutFacts(measured map[string]Fact) error {
	if len(measured) == 0 {
		return nil
	}
	now := time.Now().Unix()

	s.mu.Lock()
	if s.Facts.Hosts == nil {
		s.Facts.Hosts = map[string]Fact{}
	}
	up, slow, down := 0, 0, 0
	for host, f := range measured {
		key := strings.ToLower(host)
		old := s.Facts.Hosts[key]

		f.At = now
		// A measurement that did not run a command on the machine leaves the
		// facts alone rather than blanking them: the last known operating
		// system is better than nothing, as long as its age is kept.
		if f.OS == "" && f.Load == "" && f.Uptime == "" && f.Disk == "" && f.RAM == "" {
			f.OS, f.Load, f.Uptime = old.OS, old.Load, old.Uptime
			f.Disk, f.RAM, f.FactsAt = old.Disk, old.RAM, old.FactsAt
		} else {
			f.FactsAt = now
		}

		f.Samples = append(old.Samples, Sample{At: now, Millis: f.Millis, OK: f.Reachable()})
		if len(f.Samples) > samplesKept {
			f.Samples = f.Samples[len(f.Samples)-samplesKept:]
		}
		s.Facts.Hosts[key] = f

		switch {
		case !f.Reachable():
			down++
		case f.Slow():
			slow++
		default:
			up++
		}
	}
	s.Facts.Sweeps = append(s.Facts.Sweeps, Sweep{At: now, Up: up, Slow: slow, Down: down})
	if len(s.Facts.Sweeps) > sweepsKept {
		s.Facts.Sweeps = s.Facts.Sweeps[len(s.Facts.Sweeps)-sweepsKept:]
	}
	s.mu.Unlock()

	return writeJSON(path("facts.json"), &s.Facts)
}

// Sweeps returns the fleet's recent runs, oldest first.
func (s *Store) Sweeps() []Sweep {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sweep(nil), s.Facts.Sweeps...)
}

// FleetHealth counts what the last measurement of each host says, over the
// hosts named. Hosts never measured are counted as unknown rather than as up,
// because an unmeasured host is not evidence of anything.
func (s *Store) FleetHealth(hosts []string) (up, slow, down, unknown int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range hosts {
		f, ok := s.Facts.Hosts[strings.ToLower(h)]
		switch {
		case !ok || !f.Measured():
			unknown++
		case !f.Reachable():
			down++
		case f.Slow():
			slow++
		default:
			up++
		}
	}
	return
}

// RecordEvent notes something that happened to a host.
func (s *Store) RecordEvent(host, what string) error {
	s.mu.Lock()
	if s.Facts.Events == nil {
		s.Facts.Events = map[string][]Event{}
	}
	key := strings.ToLower(host)
	list := append(s.Facts.Events[key], Event{At: time.Now().Unix(), What: what})
	if len(list) > eventsKept {
		list = list[len(list)-eventsKept:]
	}
	s.Facts.Events[key] = list
	s.mu.Unlock()
	return writeJSON(path("facts.json"), &s.Facts)
}

// Events returns what happened to a host, newest first.
func (s *Store) Events(host string) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.Facts.Events[strings.ToLower(host)]
	out := make([]Event, len(list))
	for i, e := range list {
		out[len(list)-1-i] = e
	}
	return out
}

// RenameFacts follows a host through a rename, so that a renamed host keeps its
// measurements instead of looking like one nobody has ever reached.
func (s *Store) RenameFacts(oldName, newName string) error {
	s.mu.Lock()
	from, to := strings.ToLower(oldName), strings.ToLower(newName)
	if f, ok := s.Facts.Hosts[from]; ok {
		delete(s.Facts.Hosts, from)
		if s.Facts.Hosts == nil {
			s.Facts.Hosts = map[string]Fact{}
		}
		s.Facts.Hosts[to] = f
	}
	if e, ok := s.Facts.Events[from]; ok {
		delete(s.Facts.Events, from)
		if s.Facts.Events == nil {
			s.Facts.Events = map[string][]Event{}
		}
		s.Facts.Events[to] = e
	}
	s.mu.Unlock()
	return writeJSON(path("facts.json"), &s.Facts)
}

// MeasuredHosts returns the hosts with a measurement, newest first, which is
// what the sessions view lists.
func (s *Store) MeasuredHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for h := range s.Facts.Hosts {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return s.Facts.Hosts[out[i]].At > s.Facts.Hosts[out[j]].At
	})
	return out
}
