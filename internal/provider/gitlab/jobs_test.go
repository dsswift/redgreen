package gitlab

import (
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

func TestGitLabJobsMapToNeutralJobs(t *testing.T) {
	created := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	started := created.Add(10 * time.Minute)
	finished := started.Add(2 * time.Minute)
	queued := 30.0
	shared := &struct {
		IsShared bool `json:"is_shared"`
	}{IsShared: true}

	j, ok := glJob{ID: 7, Status: "success", CreatedAt: created, StartedAt: &started, FinishedAt: &finished, QueuedDuration: &queued, Runner: shared}.job("cloud/app", false)
	if !ok || j.ID != "gitlab:7" || j.Status != provider.JobCompleted || j.Conclusion != provider.Success {
		t.Fatalf("finished job = %+v", j)
	}
	if got := j.StartedAt.Sub(j.CreatedAt); got != 30*time.Second {
		t.Fatalf("the wait must be the queued duration, not since the pipeline made the job: %v", got)
	}
	if j.Runner != provider.RunnerSelfHosted || j.OS != "linux" {
		t.Fatalf("a shared runner on a self-managed instance is the estate's: %+v", j)
	}
	if j, _ := (glJob{ID: 8, Status: "running", StartedAt: &started, Runner: shared}).job("x/y", true); j.Runner != provider.RunnerHosted {
		t.Fatalf("a shared runner on gitlab.com is hosted: %+v", j)
	}
	if j, _ := (glJob{ID: 9, Status: "pending", CreatedAt: created}).job("x/y", false); j.Status != provider.JobQueued || !j.CreatedAt.Equal(created) {
		t.Fatalf("pending = %+v", j)
	}
	for _, s := range []string{"created", "manual", "skipped", "scheduled", "waiting_for_resource"} {
		if _, ok := (glJob{ID: 10, Status: s}).job("x/y", false); ok {
			t.Fatalf("%s is not waiting for a runner and must be left out", s)
		}
	}
	if j, _ := (glJob{ID: 11, Status: "canceled", TagList: []string{"windows-2022"}}).job("x/y", false); j.Conclusion != provider.Cancelled || j.OS != "windows" {
		t.Fatalf("cancelled windows = %+v", j)
	}
}
