package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

// jobsRead is how many of a project's newest jobs each sync reads. A sync
// runs every few minutes, so this covers far more jobs than run between two.
const jobsRead = 50

// glJob is one job of the project jobs API.
type glJob struct {
	ID             int64      `json:"id"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
	QueuedDuration *float64   `json:"queued_duration"`
	TagList        []string   `json:"tag_list"`
	Runner         *struct {
		IsShared bool `json:"is_shared"`
	} `json:"runner"`
}

// Jobs lists the project's newest jobs. GitLab sends job events only to
// webhooks a person sets on each project, so they are read instead.
func (p *Provider) Jobs(ctx context.Context, _ provider.Account, r *provider.Repo) ([]provider.Job, error) {
	id := strings.TrimPrefix(r.ID, "gitlab:")
	listed, err := one[[]glJob](ctx, p.client(), p.api("projects/"+id+"/jobs", q("per_page", strconv.Itoa(jobsRead))))
	if provider.IsStatus(err, 403, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	out := make([]provider.Job, 0, len(listed))
	for _, j := range listed {
		if job, ok := j.job(r.FullName, p.hostedRunners()); ok {
			out = append(out, job)
		}
	}
	return out, nil
}

// hostedRunners says whether the instance's shared runners are GitLab's own
// machines, which is so only on gitlab.com. On a self-managed instance
// every runner is the estate's.
func (p *Provider) hostedRunners() bool {
	u, err := url.Parse(p.cfg.URL)
	return err == nil && strings.EqualFold(u.Hostname(), "gitlab.com")
}

// job is the neutral job. Jobs that wait on an earlier stage, a person, or a
// schedule are not waiting for a runner, and are left out until they are.
func (j glJob) job(repo string, sharedAreHosted bool) (provider.Job, bool) {
	out := provider.Job{ID: "gitlab:" + strconv.FormatInt(j.ID, 10), Repo: repo, Runner: provider.RunnerSelfHosted, OS: jobOS(j.TagList)}
	if j.Runner != nil && j.Runner.IsShared && sharedAreHosted {
		out.Runner = provider.RunnerHosted
	}
	switch j.Status {
	case "pending":
		out.Status = provider.JobQueued
	case "running":
		out.Status = provider.JobInProgress
	case "success":
		out.Status, out.Conclusion = provider.JobCompleted, provider.Success
	case "failed":
		out.Status, out.Conclusion = provider.JobCompleted, provider.Failure
	case "canceled":
		out.Status, out.Conclusion = provider.JobCompleted, provider.Cancelled
	default:
		return provider.Job{}, false
	}
	if j.StartedAt != nil {
		out.StartedAt = *j.StartedAt
	}
	if j.FinishedAt != nil {
		out.CompletedAt = *j.FinishedAt
	}
	// created_at is when the pipeline made the job, which for a later stage
	// is long before it could run. queued_duration is its real wait.
	switch {
	case j.StartedAt != nil && j.QueuedDuration != nil:
		out.CreatedAt = j.StartedAt.Add(-time.Duration(*j.QueuedDuration * float64(time.Second)))
	case j.StartedAt == nil && out.Status == provider.JobQueued:
		out.CreatedAt = j.CreatedAt
	}
	return out, true
}

// jobOS reads a runner tag naming an OS; GitLab runners are Linux unless
// tagged otherwise.
func jobOS(tags []string) string {
	for _, t := range tags {
		t = strings.ToLower(t)
		switch {
		case strings.Contains(t, "macos"):
			return "macos"
		case strings.Contains(t, "windows"):
			return "windows"
		}
	}
	return "linux"
}
