package azuredevops

import (
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

func TestABuildTimelineBecomesJobs(t *testing.T) {
	queued := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	first, firstDone := queued.Add(40*time.Second), queued.Add(3*time.Minute)
	second := firstDone.Add(5 * time.Second)
	b := jobsBuild{ID: 5, Status: "inProgress", QueueTime: queued}
	records := []timelineRecord{
		{ID: "stage", Type: "Stage", State: "inProgress"},
		{ID: "deploy", Type: "Job", State: "inProgress", Order: 2, StartTime: &second, WorkerName: "build-agent-01"},
		{ID: "build", Type: "Job", State: "completed", Result: "succeeded", Order: 1, StartTime: &first, FinishTime: &firstDone, WorkerName: "Hosted Agent"},
		{ID: "later", Type: "Job", State: "pending", Order: 3},
		{ID: "skipped", Type: "Job", State: "completed", Result: "skipped", Order: 4},
	}
	jobs := buildJobs("org/proj/app", b, records)
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v", jobs)
	}
	built, deploy := jobs[0], jobs[1]
	if built.ID != "azuredevops:5:build" || built.Runner != provider.RunnerHosted || built.Conclusion != provider.Success || built.StartedAt.Sub(built.CreatedAt) != 40*time.Second {
		t.Fatalf("first job = %+v", built)
	}
	if deploy.Runner != provider.RunnerSelfHosted || deploy.Status != provider.JobInProgress || !deploy.CreatedAt.IsZero() {
		t.Fatalf("a later job waited on the jobs before it, so its wait is unknown: %+v", deploy)
	}
}

func TestABuildNoAgentHasTakenIsOneWaitingJob(t *testing.T) {
	queued := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	hosted := &struct {
		Pool *struct {
			IsHosted bool `json:"isHosted"`
		} `json:"pool"`
	}{Pool: &struct {
		IsHosted bool `json:"isHosted"`
	}{IsHosted: true}}
	b := jobsBuild{ID: 6, Status: "notStarted", QueueTime: queued, Queue: hosted}
	jobs := buildJobs("org/proj/app", b, []timelineRecord{
		{ID: "a", Type: "Job", State: "pending", Order: 1},
		{ID: "b", Type: "Job", State: "pending", Order: 2},
	})
	if len(jobs) != 1 || jobs[0].Status != provider.JobQueued || jobs[0].Runner != provider.RunnerHosted || !jobs[0].CreatedAt.Equal(queued) {
		t.Fatalf("jobs = %+v", jobs)
	}
}
