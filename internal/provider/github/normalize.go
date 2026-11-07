// Package github reads GitHub through a GitHub App, or a personal token for a
// local run. Its answers are normalised into the provider model here and
// nowhere else.
package github

import (
	"strconv"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

// The shapes of the REST answers redgreen reads. Only the fields used are
// declared, so the facts kept for a conditional request are only these.

type ghOwner struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Type    string `json:"type"`
	HTMLURL string `json:"html_url"`
}

type ghRepo struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	FullName      string     `json:"full_name"`
	HTMLURL       string     `json:"html_url"`
	Description   string     `json:"description"`
	DefaultBranch string     `json:"default_branch"`
	Visibility    string     `json:"visibility"`
	Private       bool       `json:"private"`
	Archived      bool       `json:"archived"`
	Fork          bool       `json:"fork"`
	Language      string     `json:"language"`
	PushedAt      *time.Time `json:"pushed_at"`
	Stars         int        `json:"stargazers_count"`
	OpenIssues    int        `json:"open_issues_count"`
	Owner         ghOwner    `json:"owner"`
	Topics        []string   `json:"topics"`
}

type ghWorkflow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ghRun struct {
	ID           int64     `json:"id"`
	RunNumber    int       `json:"run_number"`
	WorkflowID   int64     `json:"workflow_id"`
	DisplayTitle string    `json:"display_title"`
	Event        string    `json:"event"`
	HeadBranch   *string   `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	Status       *string   `json:"status"`
	Conclusion   *string   `json:"conclusion"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	HTMLURL      string    `json:"html_url"`
	Actor        *struct {
		Login string `json:"login"`
	} `json:"actor"`
}

type ghDeployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
	Creator     *struct {
		Login string `json:"login"`
	} `json:"creator"`
}

type ghDeploymentStatus struct {
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	LogURL    string    `json:"log_url"`
}

type ghRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	PublishedAt *time.Time `json:"published_at"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghPull struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      *ghUser   `json:"user"`
	Labels    []ghLabel `json:"labels"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	HTMLURL     string    `json:"html_url"`
	User        *ghUser   `json:"user"`
	Labels      []ghLabel `json:"labels"`
	Assignees   []ghUser  `json:"assignees"`
	Comments    int       `json:"comments"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
	Milestone   *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	Type *struct {
		Name string `json:"name"`
	} `json:"type"`
}

type ghCheckRun struct {
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
}

type ghCombinedStatus struct {
	State      string `json:"state"`
	TotalCount int    `json:"total_count"`
}

type ghDependabotAlert struct {
	SecurityVulnerability struct {
		Severity string `json:"severity"`
	} `json:"security_vulnerability"`
}

type ghCodeScanningAlert struct {
	Rule struct {
		SecuritySeverityLevel *string `json:"security_severity_level"`
		Severity              *string `json:"severity"`
	} `json:"rule"`
}

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  *struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
		Committer *struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"author"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// deployment reads a deployment's standing from its newest status that says
// how it ended. A later "inactive" only means a newer deployment replaced it.
func deployment(d ghDeployment, statuses []ghDeploymentStatus) provider.Deployment {
	out := provider.Deployment{ID: strconv.FormatInt(d.ID, 10), Environment: d.Environment, Status: provider.DeployOther, Ref: d.Ref, SHA: d.SHA, CreatedAt: d.CreatedAt}
	if d.Creator != nil {
		out.Actor = d.Creator.Login
	}
	for _, s := range statuses {
		if s.State == "success" || s.State == "failure" || s.State == "error" {
			out.Status = provider.DeployFailed
			if s.State == "success" {
				out.Status = provider.DeploySucceeded
			}
			at := s.CreatedAt
			out.FinishedAt = &at
			out.URL = s.LogURL
			return out
		}
	}
	for _, s := range statuses {
		if s.State == "in_progress" || s.State == "queued" || s.State == "pending" {
			out.Status = provider.DeployRunning
		}
	}
	return out
}

var conclusions = map[string]bool{
	provider.Success: true, provider.Failure: true, provider.Cancelled: true, provider.Skipped: true, provider.Neutral: true,
	provider.TimedOut: true, provider.ActionRequired: true, provider.StartupFailure: true, provider.Stale: true,
}

// pipelineState maps a workflow state; ok is false for a deleted workflow.
func pipelineState(state string) (string, bool) {
	switch state {
	case "active":
		return provider.Enabled, true
	case "disabled_inactivity":
		return provider.Dormant, true
	case "deleted":
		return "", false
	}
	return provider.Disabled, true
}

func run(r ghRun, tags map[string]bool) provider.Run {
	status := provider.StatusRunning
	switch str(r.Status) {
	case "completed":
		status = provider.StatusCompleted
	case "queued", "waiting", "pending":
		status = provider.StatusQueued
	}
	out := provider.Run{
		ID: strconv.FormatInt(r.ID, 10), Number: r.RunNumber, Title: r.DisplayTitle, Trigger: trigger(r, tags),
		Ref: str(r.HeadBranch), SHA: r.HeadSHA, Status: status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, URL: r.HTMLURL,
	}
	if c := str(r.Conclusion); conclusions[c] {
		out.Conclusion = c
	}
	if r.Actor != nil {
		out.Actor = r.Actor.Login
	}
	return out
}

func trigger(r ghRun, tags map[string]bool) string {
	switch r.Event {
	case "push":
		if r.HeadBranch != nil && tags[*r.HeadBranch] {
			return provider.TriggerTag
		}
		return provider.TriggerPush
	case "pull_request", "pull_request_target":
		return provider.TriggerPR
	case "schedule":
		return provider.TriggerSchedule
	case "release":
		return provider.TriggerRelease
	case "workflow_dispatch":
		return provider.TriggerManual
	}
	return provider.TriggerOther
}

func visibility(r ghRepo) string {
	if r.Visibility == "internal" {
		return "internal"
	}
	if r.Private {
		return "private"
	}
	return "public"
}

// checksState folds check runs and legacy commit statuses on one commit into one state.
func checksState(runs []ghCheckRun, status ghCombinedStatus) string {
	for _, c := range runs {
		switch str(c.Conclusion) {
		case "failure", "timed_out", "startup_failure":
			return provider.ChecksFailure
		}
	}
	if status.State == "failure" || status.State == "error" {
		return provider.ChecksFailure
	}
	for _, c := range runs {
		if c.Status != "completed" {
			return provider.ChecksPending
		}
	}
	if status.TotalCount > 0 && status.State == "pending" {
		return provider.ChecksPending
	}
	if len(runs)+status.TotalCount > 0 {
		return provider.ChecksSuccess
	}
	return provider.ChecksNone
}

func dependabotCounts(alerts []ghDependabotAlert, url string) *provider.Alerts {
	out := &provider.Alerts{URL: url}
	for _, a := range alerts {
		switch a.SecurityVulnerability.Severity {
		case "critical":
			out.Critical++
		case "high":
			out.High++
		}
	}
	return out
}

func codeScanningCounts(alerts []ghCodeScanningAlert, url string) *provider.Alerts {
	out := &provider.Alerts{URL: url}
	for _, a := range alerts {
		level := str(a.Rule.SecuritySeverityLevel)
		if a.Rule.SecuritySeverityLevel == nil && str(a.Rule.Severity) == "error" {
			level = "high"
		}
		switch level {
		case "critical":
			out.Critical++
		case "high":
			out.High++
		}
	}
	return out
}

func names(labels []ghLabel) []string {
	var out []string
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

func issue(i ghIssue) provider.Issue {
	out := provider.Issue{Number: i.Number, Title: i.Title, URL: i.HTMLURL, Labels: names(i.Labels), Comments: i.Comments, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
	if i.User != nil {
		out.Author = i.User.Login
	}
	for _, a := range i.Assignees {
		out.Assignees = append(out.Assignees, a.Login)
	}
	if i.Milestone != nil {
		out.Milestone = i.Milestone.Title
	}
	if i.Type != nil {
		out.Type = i.Type.Name
	}
	return out
}
