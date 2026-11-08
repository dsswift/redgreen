package jobs

import (
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func job(status string, started, completed time.Time) provider.Job {
	return provider.Job{ID: "1", Repo: "acme/app", Status: status, Conclusion: map[bool]string{true: "success"}[status == provider.JobCompleted],
		Runner: provider.RunnerHosted, OS: "linux", CreatedAt: t0, StartedAt: started, CompletedAt: completed}
}

func active(tr *Tracker, status string) int {
	n := 0
	for _, c := range tr.Active(t0) {
		if c.Status == status {
			n += c.N
		}
	}
	return n
}

func TestAJobWaitsRunsAndFinishesOnce(t *testing.T) {
	tr := New()
	if c := tr.Observe(job(provider.JobQueued, time.Time{}, time.Time{}), t0); c.Started || c.Finished {
		t.Fatalf("queued = %+v", c)
	}
	if active(tr, provider.JobQueued) != 1 {
		t.Fatal("a queued job must count as waiting")
	}
	c := tr.Observe(job(provider.JobInProgress, t0.Add(40*time.Second), time.Time{}), t0)
	if !c.Started || c.Wait != 40*time.Second {
		t.Fatalf("in progress = %+v", c)
	}
	if active(tr, provider.JobQueued) != 0 || active(tr, provider.JobInProgress) != 1 {
		t.Fatal("a started job must count as running, not waiting")
	}
	c = tr.Observe(job(provider.JobCompleted, t0.Add(40*time.Second), t0.Add(100*time.Second)), t0)
	if !c.Finished || c.Started || c.Ran != time.Minute || c.Conclusion != "success" {
		t.Fatalf("completed = %+v", c)
	}
	if len(tr.Active(t0)) != 0 {
		t.Fatal("a completed job must not count as active")
	}
	if c := tr.Observe(job(provider.JobCompleted, t0.Add(40*time.Second), t0.Add(100*time.Second)), t0); c.Finished {
		t.Fatal("a job must finish once")
	}
}

func TestALateStartNeverReopensAFinishedJob(t *testing.T) {
	tr := New()
	c := tr.Observe(job(provider.JobCompleted, t0.Add(10*time.Second), t0.Add(70*time.Second)), t0)
	if !c.Started || c.Wait != 10*time.Second || !c.Finished || c.Ran != time.Minute {
		t.Fatalf("completion seen first = %+v", c)
	}
	if c := tr.Observe(job(provider.JobInProgress, t0.Add(10*time.Second), time.Time{}), t0); c.Started || c.Finished {
		t.Fatalf("a late start counted again: %+v", c)
	}
	if len(tr.Active(t0)) != 0 {
		t.Fatal("a late start reopened a finished job")
	}
}

func TestACancelledJobThatNeverRanHasNoRunTime(t *testing.T) {
	tr := New()
	j := job(provider.JobCompleted, time.Time{}, t0.Add(time.Minute))
	j.Conclusion = "cancelled"
	c := tr.Observe(j, t0)
	if c.Started || !c.Finished || c.Ran != 0 || c.Conclusion != "cancelled" {
		t.Fatalf("cancelled = %+v", c)
	}
}

func TestAJobWhoseCompletionNeverCameIsForgotten(t *testing.T) {
	tr := New()
	tr.Observe(job(provider.JobQueued, time.Time{}, time.Time{}), t0)
	if n := len(tr.Active(t0.Add(forget + time.Minute))); n != 0 {
		t.Fatalf("%d stale jobs still active", n)
	}
}
