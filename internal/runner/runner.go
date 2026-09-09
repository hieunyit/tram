// Package runner fans a command out across many hosts and collects the
// results, bounded by a concurrency limit and a per-host timeout.
package runner

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hieuny/tram/internal/probe"
)

// Job is one host to work on.
type Job struct {
	Host string
	// Chain is the resolved route, used by the doctor task.
	Data any
}

// Task does the work for one host.
type Task func(ctx context.Context, job Job) probe.Result

// Options bound a fan-out.
type Options struct {
	// Parallel is how many hosts are worked on at once. Fanning out without a
	// limit is how a fleet command turns into a denial of service against your
	// own bastion.
	Parallel int
	// Timeout bounds each host, not the run as a whole.
	Timeout time.Duration
	// OnResult is called as each host finishes, for streaming output. It is
	// called from a single goroutine, so it does not need locking.
	OnResult func(probe.Result)
}

// Run works through jobs and returns the results in the order the jobs were
// given, regardless of the order they finished in, so that repeated runs are
// comparable.
func Run(ctx context.Context, jobs []Job, task Task, opt Options) []probe.Result {
	if opt.Parallel < 1 {
		opt.Parallel = 5
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}

	results := make([]probe.Result, len(jobs))
	sem := make(chan struct{}, opt.Parallel)
	done := make(chan int, len(jobs))

	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func(i int, job Job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = probe.Result{Host: job.Host, Class: probe.Timeout, Detail: "cancelled"}
				done <- i
				return
			}
			defer func() { <-sem }()
			results[i] = task(ctx, job)
			done <- i
		}(i, job)
	}

	// Report results as they arrive, from one goroutine, so a caller printing
	// a stream does not have to serialise anything itself.
	var reporter sync.WaitGroup
	if opt.OnResult != nil {
		reporter.Add(1)
		go func() {
			defer reporter.Done()
			for i := range done {
				opt.OnResult(results[i])
			}
		}()
	}

	wg.Wait()
	close(done)
	reporter.Wait()
	return results
}

// Summary counts results by class, which is what a fleet command prints at the
// end and what decides the exit status.
type Summary struct {
	Total   int            `json:"total"`
	OK      int            `json:"ok"`
	Failed  int            `json:"failed"`
	ByClass map[string]int `json:"by_class"`
}

// Summarise counts a set of results.
func Summarise(rs []probe.Result) Summary {
	s := Summary{Total: len(rs), ByClass: map[string]int{}}
	for _, r := range rs {
		s.ByClass[string(r.Class)]++
		if r.Class.Good() {
			s.OK++
			continue
		}
		s.Failed++
	}
	return s
}

// Classes returns the classes present, sorted, for stable output.
func (s Summary) Classes() []string {
	out := make([]string, 0, len(s.ByClass))
	for c := range s.ByClass {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
