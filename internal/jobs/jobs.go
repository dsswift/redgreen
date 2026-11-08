// Package jobs follows CI jobs from their webhooks and polls: which are
// waiting for a runner, which are running, and what each change of state
// means for the measures. It keeps no history. A poll lists jobs that
// finished before a restart again, so a job that had started before the
// tracker did is never measured again: its wait, and a finish before the
// tracker started, were counted by the process before.
package jobs

import (
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

// forget is how long a job is kept after its last delivery. A job whose
// completion never arrives stops counting as active after it.
const forget = 24 * time.Hour

// Key is what a job is counted by.
type Key struct {
	Repo   string
	Runner string
	OS     string
}

// Count is how many jobs share a key and a status.
type Count struct {
	Key
	Status string
	N      int
}

// Change is what one delivery means for the measures.
type Change struct {
	Key
	// Started is set the first time a job is seen on a runner; Wait is how
	// long it waited for one, when WaitKnown.
	Started   bool
	WaitKnown bool
	Wait      time.Duration
	// Finished is set when a job completes; Ran is how long it ran, zero
	// when it never reached a runner.
	Finished   bool
	Conclusion string
	Ran        time.Duration
}

type entry struct {
	key     Key
	status  string
	started bool
	done    bool
	seen    time.Time
}

// Tracker holds the jobs in flight.
type Tracker struct {
	mu    sync.Mutex
	jobs  map[string]*entry
	since time.Time
}

// New is an empty tracker that measures every job.
func New() *Tracker { return &Tracker{jobs: map[string]*entry{}} }

// Since is an empty tracker that measures nothing a job did before since.
func Since(since time.Time) *Tracker { return &Tracker{jobs: map[string]*entry{}, since: since} }

// Observe takes one delivery. Deliveries can arrive out of order, so a
// state never moves a job backwards, and a job counts once.
func (t *Tracker) Observe(j provider.Job, now time.Time) Change {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	e := t.jobs[j.ID]
	if e == nil {
		e = &entry{status: provider.JobQueued}
		t.jobs[j.ID] = e
		// Seen first after a restart: what it did before was measured then.
		if !j.StartedAt.IsZero() && j.StartedAt.Before(t.since) {
			e.started = true
		}
		if !j.CompletedAt.IsZero() && j.CompletedAt.Before(t.since) {
			e.done = true
		}
	}
	e.seen = now
	if e.done {
		return Change{}
	}
	// The runner that took the job is known from in_progress on; before
	// that the key holds what the job asked for.
	e.key = Key{Repo: j.Repo, Runner: j.Runner, OS: j.OS}
	c := Change{Key: e.key}
	ran := !j.StartedAt.IsZero()
	if ran && !e.started {
		e.started = true
		c.Started = true
		if !j.CreatedAt.IsZero() {
			c.WaitKnown = true
			c.Wait = nonNegative(j.StartedAt.Sub(j.CreatedAt))
		}
	}
	switch j.Status {
	case provider.JobInProgress:
		e.status = provider.JobInProgress
	case provider.JobCompleted:
		e.done = true
		c.Finished = true
		c.Conclusion = j.Conclusion
		if ran && !j.CompletedAt.IsZero() {
			c.Ran = nonNegative(j.CompletedAt.Sub(j.StartedAt))
		}
	}
	return c
}

// Active counts the jobs waiting and running.
func (t *Tracker) Active(now time.Time) []Count {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	by := map[Count]int{}
	for _, e := range t.jobs {
		if !e.done {
			by[Count{Key: e.key, Status: e.status}]++
		}
	}
	out := make([]Count, 0, len(by))
	for c, n := range by {
		c.N = n
		out = append(out, c)
	}
	return out
}

func (t *Tracker) prune(now time.Time) {
	for id, e := range t.jobs {
		if now.Sub(e.seen) > forget {
			delete(t.jobs, id)
		}
	}
}

func nonNegative(d time.Duration) time.Duration {
	return max(d, 0)
}
