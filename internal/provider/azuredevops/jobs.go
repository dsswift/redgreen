package azuredevops

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

const (
	// jobsWindow is how far back each sync looks for builds. A build queued
	// earlier and still running is read again while it stays in the window.
	jobsWindow = 3 * time.Hour
	jobsBuilds = 20
)

type jobsBuild struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	QueueTime time.Time `json:"queueTime"`
	Queue     *struct {
		Pool *struct {
			IsHosted bool `json:"isHosted"`
		} `json:"pool"`
	} `json:"queue"`
}

type timelineRecord struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	State      string     `json:"state"`
	Result     string     `json:"result"`
	Order      int        `json:"order"`
	StartTime  *time.Time `json:"startTime"`
	FinishTime *time.Time `json:"finishTime"`
	WorkerName string     `json:"workerName"`
}

// Jobs reads the jobs of the repository's recent builds from each build's
// timeline. Azure DevOps sends job events only through service hooks a
// person sets up, so they are read instead. A finished build's timeline is
// read once.
func (p *Provider) Jobs(ctx context.Context, a provider.Account, r *provider.Repo) ([]provider.Job, error) {
	parts := strings.SplitN(r.FullName, "/", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("jobs: %q is not organization/project/repository", r.FullName)
	}
	f := p.client()
	project := p.organization(a.Login) + "/" + url.PathEscape(parts[1])
	builds, err := get[struct {
		Value []jobsBuild `json:"value"`
	}](ctx, f, project+"/_apis/build/builds", q("repositoryId", repoID(r.ID), "repositoryType", "TfsGit", "queryOrder", "queueTimeDescending",
		"minTime", time.Now().Add(-jobsWindow).UTC().Format(time.RFC3339), "$top", strconv.Itoa(jobsBuilds)))
	if provider.IsStatus(err, 403, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("builds: %w", err)
	}
	var out []provider.Job
	for _, b := range builds.Value {
		key := project + "/" + strconv.FormatInt(b.ID, 10)
		if p.timelineRead(key) {
			continue
		}
		timeline, err := once[struct {
			Records []timelineRecord `json:"records"`
		}](ctx, f, fmt.Sprintf("%s/_apis/build/builds/%d/timeline", project, b.ID), nil)
		if provider.IsStatus(err, 404) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("timeline of build %d: %w", b.ID, err)
		}
		out = append(out, buildJobs(r.FullName, b, timeline.Records)...)
		if b.Status == "completed" {
			p.markTimelineRead(key)
		}
	}
	return out, nil
}

func (p *Provider) timelineRead(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.timelinesRead[key]
	return ok
}

func (p *Provider) markTimelineRead(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, at := range p.timelinesRead {
		if now.Sub(at) > 2*jobsWindow {
			delete(p.timelinesRead, k)
		}
	}
	p.timelinesRead[key] = now
}

// buildJobs turns a build's timeline into jobs. The build's queue time is
// the wait of its first job only; a later job waited on the jobs before it,
// not on an agent, so its wait is not known. While no job has started the
// build is one job waiting for an agent.
func buildJobs(repo string, b jobsBuild, records []timelineRecord) []provider.Job {
	var jobs []timelineRecord
	for _, rec := range records {
		if rec.Type == "Job" {
			jobs = append(jobs, rec)
		}
	}
	sort.Slice(jobs, func(i, k int) bool {
		si, sk := jobs[i].StartTime, jobs[k].StartTime
		switch {
		case si == nil && sk == nil:
			return jobs[i].Order < jobs[k].Order
		case si == nil:
			return false
		case sk == nil:
			return true
		}
		return si.Before(*sk)
	})
	buildHosted := b.Queue != nil && b.Queue.Pool != nil && b.Queue.Pool.IsHosted
	var out []provider.Job
	anyStarted := false
	for i, rec := range jobs {
		j := provider.Job{ID: "azuredevops:" + strconv.FormatInt(b.ID, 10) + ":" + rec.ID, Repo: repo, Runner: agentKind(rec.WorkerName, buildHosted), OS: "unknown"}
		switch rec.State {
		case "pending":
			if anyStarted || i > 0 || b.Status != "inProgress" && b.Status != "notStarted" {
				continue
			}
			j.Status, j.CreatedAt = provider.JobQueued, b.QueueTime
		case "inProgress":
			j.Status = provider.JobInProgress
		case "completed":
			c, ok := jobResult[rec.Result]
			if !ok {
				continue
			}
			j.Status, j.Conclusion = provider.JobCompleted, c
		default:
			continue
		}
		if rec.StartTime != nil {
			j.StartedAt = *rec.StartTime
			if !anyStarted {
				j.CreatedAt = b.QueueTime
			}
			anyStarted = true
		}
		if rec.FinishTime != nil {
			j.CompletedAt = *rec.FinishTime
		}
		out = append(out, j)
	}
	return out
}

// jobResult maps a finished job's result. A skipped job never ran.
var jobResult = map[string]string{"succeeded": provider.Success, "succeededWithIssues": provider.Success, "failed": provider.Failure, "canceled": provider.Cancelled, "abandoned": provider.Cancelled}

// agentKind reads the agent that took the job: Microsoft's hosted agents
// are named "Hosted Agent" or "Azure Pipelines n". Before an agent takes
// it, the build's pool says.
func agentKind(worker string, buildHosted bool) string {
	switch {
	case worker == "":
		if buildHosted {
			return provider.RunnerHosted
		}
		return provider.RunnerSelfHosted
	case strings.HasPrefix(worker, "Hosted Agent"), strings.HasPrefix(worker, "Azure Pipelines"):
		return provider.RunnerHosted
	}
	return provider.RunnerSelfHosted
}
