// Package gitlab reads GitLab, gitlab.com or self-managed, through an OAuth
// application or an access token. Its answers are normalised into the
// provider model here and nowhere else.
package gitlab

import (
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

type glNamespace struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
}

type glGroup struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullPath string `json:"full_path"`
	WebURL   string `json:"web_url"`
}

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	WebURL   string `json:"web_url"`
}

type glProject struct {
	ID                int64       `json:"id"`
	Name              string      `json:"name"`
	PathWithNamespace string      `json:"path_with_namespace"`
	WebURL            string      `json:"web_url"`
	Description       string      `json:"description"`
	DefaultBranch     string      `json:"default_branch"`
	Visibility        string      `json:"visibility"`
	Archived          bool        `json:"archived"`
	ForkedFrom        *struct{}   `json:"forked_from_project"`
	StarCount         int         `json:"star_count"`
	OpenIssuesCount   int         `json:"open_issues_count"`
	LastActivityAt    time.Time   `json:"last_activity_at"`
	CreatedAt         time.Time   `json:"created_at"`
	BuildsAccessLevel string      `json:"builds_access_level"`
	CIConfigPath      string      `json:"ci_config_path"`
	Namespace         glNamespace `json:"namespace"`
	Topics            []string    `json:"topics"`
	TagList           []string    `json:"tag_list"`
}

type glBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID             string `json:"id"`
		AuthorName     string `json:"author_name"`
		AuthorEmail    string `json:"author_email"`
		CommitterName  string `json:"committer_name"`
		CommitterEmail string `json:"committer_email"`
		CommittedDate  string `json:"committed_date"`
		AuthoredDate   string `json:"authored_date"`
	} `json:"commit"`
}

type glProtectedBranch struct {
	CodeOwnerApprovalRequired *bool `json:"code_owner_approval_required"`
}

type glApprovalRule struct {
	ApprovalsRequired int `json:"approvals_required"`
}

type glPipeline struct {
	ID        int64     `json:"id"`
	IID       int       `json:"iid"`
	Name      *string   `json:"name"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

type glSchedule struct {
	ID           int64     `json:"id"`
	Description  string    `json:"description"`
	Cron         string    `json:"cron"`
	CronTimezone string    `json:"cron_timezone"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type glRelease struct {
	TagName         string    `json:"tag_name"`
	Name            string    `json:"name"`
	ReleasedAt      time.Time `json:"released_at"`
	UpcomingRelease bool      `json:"upcoming_release"`
	Links           struct {
		Self string `json:"self"`
	} `json:"_links"`
}

type glMergeRequest struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	WebURL    string    `json:"web_url"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Labels    []string  `json:"labels"`
	Author    *struct {
		Username string `json:"username"`
	} `json:"author"`
}

type glIssue struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	WebURL    string    `json:"web_url"`
	Labels    []string  `json:"labels"`
	Comments  int       `json:"user_notes_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IssueType string    `json:"issue_type"`
	Author    *struct {
		Username string `json:"username"`
	} `json:"author"`
	Assignees []struct {
		Username string `json:"username"`
	} `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

type glDeployment struct {
	ID          int64     `json:"id"`
	Ref         string    `json:"ref"`
	SHA         string    `json:"sha"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Environment *struct {
		Name string `json:"name"`
	} `json:"environment"`
	User *struct {
		Username string `json:"username"`
	} `json:"user"`
	Deployable *struct {
		FinishedAt *time.Time `json:"finished_at"`
		WebURL     string     `json:"web_url"`
	} `json:"deployable"`
}

type severityCount struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
}

type glSecurity struct {
	Scanners *struct {
		Enabled []string `json:"enabled"`
	} `json:"securityScanners"`
	Dependency *severityCount `json:"dependency"`
	Code       *severityCount `json:"code"`
}

// finished maps the pipeline states GitLab reports as settled. Anything else is still moving.
var finished = map[string]string{
	"success":  provider.Success,
	"failed":   provider.Failure,
	"canceled": provider.Cancelled,
	"skipped":  provider.Skipped,
	// Blocked on a manual job: says nothing about health.
	"manual": provider.ActionRequired,
}

func runStatus(status string) (string, string) {
	if c, ok := finished[status]; ok {
		return provider.StatusCompleted, c
	}
	if status == "running" || status == "canceling" {
		return provider.StatusRunning, ""
	}
	return provider.StatusQueued, ""
}

func trigger(p glPipeline, tags map[string]bool) string {
	switch p.Source {
	case "push":
		if tags[p.Ref] {
			return provider.TriggerTag
		}
		return provider.TriggerPush
	case "merge_request_event", "external_pull_request_event":
		return provider.TriggerPR
	case "schedule", "scheduled":
		return provider.TriggerSchedule
	case "web", "chat":
		return provider.TriggerManual
	}
	return provider.TriggerOther
}

func run(p glPipeline, tags map[string]bool) provider.Run {
	status, conclusion := runStatus(p.Status)
	title := p.Ref
	if p.Name != nil {
		title = *p.Name
	}
	return provider.Run{
		ID: strconv.FormatInt(p.ID, 10), Number: p.IID, Title: title, Trigger: trigger(p, tags), Ref: p.Ref, SHA: p.SHA,
		Status: status, Conclusion: conclusion, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, URL: p.WebURL,
	}
}

func ciConfigPath(p glProject) string {
	if p.CIConfigPath != "" {
		return p.CIConfigPath
	}
	return ".gitlab-ci.yml"
}

// mainPipeline is the one pipeline a project's CI configuration defines. Runs from schedules are on their own pipelines.
func mainPipeline(p glProject, state string, runs []provider.Run) provider.Pipeline {
	updated := p.LastActivityAt
	if len(runs) > 0 {
		updated = runs[0].UpdatedAt
	}
	return provider.Pipeline{ID: "ci", Name: "Pipeline", Path: ciConfigPath(p), URL: p.WebURL + "/-/pipelines", State: state, CreatedAt: p.CreatedAt, UpdatedAt: updated, Schedules: []provider.Schedule{}, Runs: runs}
}

func schedulePipeline(p glProject, s glSchedule, ciDisabled bool, runs []provider.Run) provider.Pipeline {
	state := provider.Enabled
	if ciDisabled || !s.Active {
		state = provider.Disabled
	}
	return provider.Pipeline{
		ID: "schedule:" + strconv.FormatInt(s.ID, 10), Name: s.Description, Path: ciConfigPath(p), URL: p.WebURL + "/-/pipeline_schedules",
		State: state, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		Schedules: []provider.Schedule{{Cron: strings.TrimSpace(s.Cron), TimeZone: ianaTimeZone(s.CronTimezone)}}, Runs: runs,
	}
}

// ianaTimeZone resolves GitLab's stored time zone name; unknown names fall back to UTC.
func ianaTimeZone(name string) string {
	for _, c := range []string{name, railsTimeZones[name]} {
		if c != "" && provider.ValidTimeZone(c) {
			return c
		}
	}
	return "UTC"
}

func deployment(d glDeployment) provider.Deployment {
	status := provider.DeployOther
	switch d.Status {
	case "success":
		status = provider.DeploySucceeded
	case "failed":
		status = provider.DeployFailed
	case "running", "created":
		status = provider.DeployRunning
	}
	out := provider.Deployment{ID: strconv.FormatInt(d.ID, 10), Environment: "unknown", Status: status, Ref: d.Ref, SHA: d.SHA, CreatedAt: d.CreatedAt}
	if d.Environment != nil {
		out.Environment = d.Environment.Name
	}
	if d.User != nil {
		out.Actor = d.User.Username
	}
	if d.Deployable != nil {
		out.URL = d.Deployable.WebURL
	}
	if status == provider.DeploySucceeded || status == provider.DeployFailed {
		at := d.UpdatedAt
		if d.Deployable != nil && d.Deployable.FinishedAt != nil {
			at = *d.Deployable.FinishedAt
		}
		out.FinishedAt = &at
	}
	return out
}

// checksState is a merge request's head pipeline status as a checks state.
func checksState(status string) string {
	switch status {
	case "", "canceled", "skipped":
		return provider.ChecksNone
	case "success":
		return provider.ChecksSuccess
	case "failed":
		return provider.ChecksFailure
	}
	return provider.ChecksPending
}

var (
	dependencyScanners = []string{"DEPENDENCY_SCANNING", "CONTAINER_SCANNING"}
	codeScanners       = []string{"SAST", "SAST_ADVANCED", "SAST_IAC", "SECRET_DETECTION", "DAST", "API_FUZZING", "COVERAGE_FUZZING"}
)

// securityCounts folds the security query into the two feeds the model
// knows. Nil when no scanner of that kind runs, or the tier hides counts.
func securityCounts(s *glSecurity, projectURL string) (*provider.Alerts, *provider.Alerts) {
	url := projectURL + "/-/security/vulnerability_report"
	enabled := map[string]bool{}
	if s != nil && s.Scanners != nil {
		for _, e := range s.Scanners.Enabled {
			enabled[e] = true
		}
	}
	counts := func(scanners []string, found *severityCount) *provider.Alerts {
		if found == nil {
			return nil
		}
		for _, sc := range scanners {
			if enabled[sc] {
				return &provider.Alerts{Critical: found.Critical, High: found.High, URL: url}
			}
		}
		return nil
	}
	if s == nil {
		return nil, nil
	}
	return counts(dependencyScanners, s.Dependency), counts(codeScanners, s.Code)
}

func boolp(b bool) *bool { return &b }

// facts reads branch rules, the last commit, and which well-known files sit
// at the root. A nil input is something the project would not answer; the
// facts it would have given stay unknown rather than reading as no.
func facts(p glProject, branch *glBranch, protection *glProtectedBranch, approvals []glApprovalRule, approvalsKnown bool, root []string, rootKnown bool) *provider.Facts {
	f := &provider.Facts{Topics: p.Topics}
	if f.Topics == nil {
		f.Topics = p.TagList
	}
	if f.Topics == nil {
		f.Topics = []string{}
	}
	if rootKnown {
		var readme, docker, owners bool
		for _, n := range root {
			n = strings.ToLower(n)
			readme = readme || strings.HasPrefix(n, "readme")
			docker = docker || n == "dockerfile" || strings.HasSuffix(n, ".dockerfile")
			owners = owners || n == "codeowners"
		}
		f.HasReadme, f.HasDockerfile, f.HasCodeowners = boolp(readme), boolp(docker), boolp(owners)
	}
	if approvalsKnown {
		n := 0
		requires := false
		for _, r := range approvals {
			if r.ApprovalsRequired > 0 {
				requires = true
				n = max(n, r.ApprovalsRequired)
			}
		}
		f.RequiresReview, f.RequiredApprovals = boolp(requires), &n
	}
	if protection != nil && protection.CodeOwnerApprovalRequired != nil {
		f.RequiresCodeOwnerReview = boolp(*protection.CodeOwnerApprovalRequired)
	}
	if branch != nil {
		f.BranchProtected = boolp(branch.Protected)
		c := branch.Commit
		f.LastCommitter = c.AuthorName
		if f.LastCommitter == "" {
			f.LastCommitter = c.CommitterName
		}
		email := c.AuthorEmail
		if email == "" {
			email = c.CommitterEmail
		}
		f.LastCommitterEmail = services.EmailOf(email)
		at := c.CommittedDate
		if at == "" {
			at = c.AuthoredDate
		}
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			f.LastCommitAt = &t
		}
	}
	return f
}

func issue(i glIssue) provider.Issue {
	out := provider.Issue{Number: i.IID, Title: i.Title, URL: i.WebURL, Labels: i.Labels, Comments: i.Comments, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, Type: i.IssueType}
	if i.Author != nil {
		out.Author = i.Author.Username
	}
	for _, a := range i.Assignees {
		out.Assignees = append(out.Assignees, a.Username)
	}
	if i.Milestone != nil {
		out.Milestone = i.Milestone.Title
	}
	return out
}

// topLevel keeps the groups not nested under another in the list; the parent's projects already cover them.
func topLevel(groups []glGroup) []glGroup {
	var out []glGroup
	for _, g := range groups {
		nested := false
		for _, o := range groups {
			if strings.HasPrefix(g.FullPath, o.FullPath+"/") {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, g)
		}
	}
	return out
}
