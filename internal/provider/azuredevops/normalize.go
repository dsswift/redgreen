// Package azuredevops reads Azure DevOps through an Entra application that
// is a member of each organisation. Its answers are normalised into the
// provider model here and nowhere else.
package azuredevops

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"gopkg.in/yaml.v3"
)

type adoRepo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WebURL        string `json:"webUrl"`
	DefaultBranch string `json:"defaultBranch"`
	IsDisabled    bool   `json:"isDisabled"`
	IsFork        bool   `json:"isFork"`
	Project       struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Visibility string `json:"visibility"`
	} `json:"project"`
}

// adoDefinitionSchedule is a schedule set in the pipeline editor rather than in the YAML file.
type adoDefinitionSchedule struct {
	TimeZoneID   string `json:"timeZoneId"`
	StartHours   int    `json:"startHours"`
	StartMinutes int    `json:"startMinutes"`
	// DaysToBuild is a bit per weekday, or the weekday names when the service writes the flags out.
	DaysToBuild             any  `json:"daysToBuild"`
	ScheduleOnlyWithChanges bool `json:"scheduleOnlyWithChanges"`
}

type adoDefinition struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	QueueStatus string    `json:"queueStatus"`
	Quality     string    `json:"quality"`
	CreatedDate time.Time `json:"createdDate"`
	Process     *struct {
		Type         int    `json:"type"`
		YAMLFilename string `json:"yamlFilename"`
	} `json:"process"`
	Triggers []struct {
		TriggerType string                  `json:"triggerType"`
		Schedules   []adoDefinitionSchedule `json:"schedules"`
	} `json:"triggers"`
	Links *struct {
		Web *struct {
			Href string `json:"href"`
		} `json:"web"`
	} `json:"_links"`
}

type adoBuild struct {
	ID          int64      `json:"id"`
	BuildNumber string     `json:"buildNumber"`
	Status      string     `json:"status"`
	Result      string     `json:"result"`
	QueueTime   time.Time  `json:"queueTime"`
	StartTime   *time.Time `json:"startTime"`
	FinishTime  *time.Time `json:"finishTime"`
	Definition  struct {
		ID int64 `json:"id"`
	} `json:"definition"`
	SourceBranch  string `json:"sourceBranch"`
	SourceVersion string `json:"sourceVersion"`
	Reason        string `json:"reason"`
	Repository    *struct {
		ID string `json:"id"`
	} `json:"repository"`
	RequestedFor *struct {
		DisplayName string `json:"displayName"`
	} `json:"requestedFor"`
	Links *struct {
		Web *struct {
			Href string `json:"href"`
		} `json:"web"`
	} `json:"_links"`
}

type adoPullRequest struct {
	PullRequestID int       `json:"pullRequestId"`
	Title         string    `json:"title"`
	IsDraft       bool      `json:"isDraft"`
	CreationDate  time.Time `json:"creationDate"`
	CreatedBy     *struct {
		DisplayName string `json:"displayName"`
	} `json:"createdBy"`
}

type adoPolicy struct {
	IsEnabled  bool `json:"isEnabled"`
	IsBlocking bool `json:"isBlocking"`
	IsDeleted  bool `json:"isDeleted"`
	Type       struct {
		ID string `json:"id"`
	} `json:"type"`
	Settings *struct {
		MinimumApproverCount int `json:"minimumApproverCount"`
	} `json:"settings"`
}

type adoAlert struct {
	AlertType string `json:"alertType"`
	Severity  string `json:"severity"`
}

type adoLanguageMetrics struct {
	Repositories []struct {
		ID        string `json:"id"`
		Breakdown []struct {
			Name       string   `json:"name"`
			Percentage *float64 `json:"languagePercentage"`
		} `json:"languageBreakdown"`
	} `json:"repositoryLanguageAnalytics"`
}

type adoCommit struct {
	CommitID string `json:"commitId"`
	Comment  string `json:"comment"`
	Author   *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Date  string `json:"date"`
	} `json:"author"`
	Committer *struct {
		Date string `json:"date"`
	} `json:"committer"`
}

// Policy types every organisation shares.
const (
	minimumReviewersPolicy  = "fa4e907d-c16b-4a4c-9dfa-4906e5d171dd"
	requiredReviewersPolicy = "fd2167ab-b0be-447a-8ec8-39368250530e"
)

var refPrefix = regexp.MustCompile(`^refs/(heads/|tags/)?`)

// shortRef is refs/heads/main as main, refs/tags/v1 as v1. Other refs keep everything after refs/.
func shortRef(ref string) string { return refPrefix.ReplaceAllString(ref, "") }

func visibility(r adoRepo) string {
	if r.Project.Visibility == "public" {
		return "public"
	}
	return "private"
}

// finished maps the results Azure Pipelines reports for a finished build.
// Partially succeeded finished with a tolerated step failure.
var finished = map[string]string{"succeeded": provider.Success, "partiallySucceeded": provider.Success, "failed": provider.Failure, "canceled": provider.Cancelled}

func runStatus(status, result string) (string, string) {
	if status == "completed" {
		if c, ok := finished[result]; ok {
			return provider.StatusCompleted, c
		}
		return provider.StatusCompleted, provider.Neutral
	}
	if status == "inProgress" || status == "cancelling" {
		return provider.StatusRunning, ""
	}
	return provider.StatusQueued, ""
}

func trigger(reason, sourceBranch string) string {
	switch reason {
	case "individualCI", "batchedCI":
		if strings.HasPrefix(sourceBranch, "refs/tags/") {
			return provider.TriggerTag
		}
		return provider.TriggerPush
	case "pullRequest":
		return provider.TriggerPR
	case "schedule":
		return provider.TriggerSchedule
	case "manual", "userCreated":
		return provider.TriggerManual
	}
	return provider.TriggerOther
}

func run(b adoBuild) provider.Run {
	status, conclusion := runStatus(b.Status, b.Result)
	updated := b.QueueTime
	if b.FinishTime != nil {
		updated = *b.FinishTime
	} else if b.StartTime != nil {
		updated = *b.StartTime
	}
	out := provider.Run{
		ID: strconv.FormatInt(b.ID, 10), Number: int(b.ID), Title: b.BuildNumber, Trigger: trigger(b.Reason, b.SourceBranch),
		Ref: shortRef(b.SourceBranch), SHA: b.SourceVersion, Status: status, Conclusion: conclusion, CreatedAt: b.QueueTime, UpdatedAt: updated,
	}
	if b.Links != nil && b.Links.Web != nil {
		out.URL = b.Links.Web.Href
	}
	if b.RequestedFor != nil {
		out.Actor = b.RequestedFor.DisplayName
	}
	return out
}

// pipelineState reads a paused pipeline as disabled: it queues runs without starting them.
func pipelineState(d adoDefinition) string {
	if d.QueueStatus == "paused" || d.QueueStatus == "disabled" {
		return provider.Disabled
	}
	return provider.Enabled
}

func pipeline(d adoDefinition, projectURL string, schedules []provider.Schedule, runs []provider.Run) provider.Pipeline {
	path := "classic pipeline"
	if d.Process != nil && d.Process.YAMLFilename != "" {
		path = d.Process.YAMLFilename
	}
	u := fmt.Sprintf("%s/_build?definitionId=%d", projectURL, d.ID)
	if d.Links != nil && d.Links.Web != nil {
		u = d.Links.Web.Href
	}
	return provider.Pipeline{ID: strconv.FormatInt(d.ID, 10), Name: d.Name, Path: path, URL: u, State: pipelineState(d), CreatedAt: d.CreatedDate, UpdatedAt: d.CreatedDate, Schedules: schedules, Runs: runs}
}

// FileSchedules is what a pipeline's schedules say.
type FileSchedules struct {
	// Schedules fire whether or not the branch changed.
	Schedules []provider.Schedule
	// Conditional counts the schedules that fire only after a change; a missing run from one says nothing.
	Conditional int
}

// definitionSchedules reads the schedules set in the pipeline editor; nil
// when there are none and the YAML file decides.
func definitionSchedules(d adoDefinition) *FileSchedules {
	var all []adoDefinitionSchedule
	for _, t := range d.Triggers {
		if t.TriggerType == "schedule" {
			all = append(all, t.Schedules...)
		}
	}
	if len(all) == 0 {
		return nil
	}
	out := &FileSchedules{Schedules: []provider.Schedule{}}
	for _, s := range all {
		if s.ScheduleOnlyWithChanges {
			out.Conditional++
			continue
		}
		out.Schedules = append(out.Schedules, provider.Schedule{Cron: fmt.Sprintf("%d %d * * %s", s.StartMinutes, s.StartHours, cronDays(s.DaysToBuild)), TimeZone: ianaTimeZone(s.TimeZoneID)})
	}
	return out
}

// weekdays in the order of their bits, lowest first, with the number cron knows each by.
var weekdays = []struct {
	name string
	cron int
}{{"monday", 1}, {"tuesday", 2}, {"wednesday", 3}, {"thursday", 4}, {"friday", 5}, {"saturday", 6}, {"sunday", 0}}

// cronDays is the day-of-week field of a cron expression for a set of weekdays.
func cronDays(days any) string {
	var chosen []string
	for bit, d := range weekdays {
		on := false
		switch v := days.(type) {
		case float64:
			on = int(v)&(1<<bit) != 0
		case int:
			on = v&(1<<bit) != 0
		case string:
			for _, n := range strings.Split(strings.ToLower(v), ",") {
				n = strings.TrimSpace(n)
				on = on || n == d.name || n == "all"
			}
		}
		if on {
			chosen = append(chosen, strconv.Itoa(d.cron))
		}
	}
	if len(chosen) == 0 || len(chosen) == len(weekdays) {
		return "*"
	}
	return strings.Join(chosen, ",")
}

// ianaTimeZone resolves a Windows time zone name; unknown names fall back to UTC.
func ianaTimeZone(name string) string {
	for _, c := range []string{windowsTimeZones[name], name} {
		if c != "" && provider.ValidTimeZone(c) {
			return c
		}
	}
	return "UTC"
}

// ParseSchedules reads the schedules an Azure Pipelines YAML file declares.
// Only those marked always fire on a quiet branch; they are evaluated in UTC.
func ParseSchedules(text string) FileSchedules {
	out := FileSchedules{Schedules: []provider.Schedule{}}
	var doc struct {
		Schedules []map[string]any `yaml:"schedules"`
	}
	if yaml.Unmarshal([]byte(text), &doc) != nil {
		return out
	}
	for _, e := range doc.Schedules {
		cron, ok := e["cron"].(string)
		if !ok {
			continue
		}
		if always, _ := e["always"].(bool); always {
			out.Schedules = append(out.Schedules, provider.Schedule{Cron: strings.TrimSpace(cron), TimeZone: "UTC"})
		} else {
			out.Conditional++
		}
	}
	return out
}

// checksState reads a pull request's checks from the builds of its merge ref:
// the newest build of each pipeline decides.
func checksState(builds []adoBuild) string {
	sorted := append([]adoBuild{}, builds...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].QueueTime.After(sorted[j].QueueTime) })
	seen := map[int64]bool{}
	var pending, success bool
	for _, b := range sorted {
		if seen[b.Definition.ID] {
			continue
		}
		seen[b.Definition.ID] = true
		status, conclusion := runStatus(b.Status, b.Result)
		if conclusion == provider.Failure {
			return provider.ChecksFailure
		}
		pending = pending || status != provider.StatusCompleted
		success = success || conclusion == provider.Success
	}
	switch {
	case pending:
		return provider.ChecksPending
	case success:
		return provider.ChecksSuccess
	}
	return provider.ChecksNone
}

// securityCounts folds Advanced Security alerts into the two feeds the model knows. Secrets count with code.
func securityCounts(alerts []adoAlert, repoURL string) (*provider.Alerts, *provider.Alerts) {
	url := repoURL + "/alerts"
	dep, code := &provider.Alerts{URL: url}, &provider.Alerts{URL: url}
	for _, a := range alerts {
		target := code
		if a.AlertType == "dependency" || a.AlertType == "malware" {
			target = dep
		}
		switch a.Severity {
		case "critical":
			target.Critical++
		case "high":
			target.High++
		}
	}
	return dep, code
}

// topLanguages is the main language per repository id. Entries without a share are data or binary files.
func topLanguages(m adoLanguageMetrics) map[string]string {
	out := map[string]string{}
	for _, r := range m.Repositories {
		shares := map[string]float64{}
		for _, l := range r.Breakdown {
			if l.Percentage != nil {
				shares[l.Name] = *l.Percentage
			}
		}
		out[r.ID] = services.MainLanguage(shares)
	}
	return out
}

func boolp(b bool) *bool { return &b }

// facts reads branch rules from the policies on the default branch. A nil
// input is a part the application may not read.
func facts(policies []adoPolicy, policiesKnown bool, root []string, rootKnown bool, commit *adoCommit) *provider.Facts {
	f := &provider.Facts{Topics: []string{}}
	if policiesKnown {
		var enabled, reviewers []adoPolicy
		for _, p := range policies {
			if p.IsEnabled && !p.IsDeleted {
				enabled = append(enabled, p)
				if p.Type.ID == minimumReviewersPolicy {
					reviewers = append(reviewers, p)
				}
			}
		}
		blocking, codeOwners := false, false
		for _, p := range enabled {
			blocking = blocking || p.IsBlocking
			// The nearest thing to code owners: reviewers a policy adds by path and requires.
			codeOwners = codeOwners || (p.Type.ID == requiredReviewersPolicy && p.IsBlocking)
		}
		f.BranchProtected, f.RequiresReview, f.RequiresCodeOwnerReview = boolp(blocking), boolp(len(reviewers) > 0), boolp(codeOwners)
		if len(reviewers) > 0 {
			n := 0
			for _, p := range reviewers {
				if p.Settings != nil {
					n = max(n, p.Settings.MinimumApproverCount)
				}
			}
			f.RequiredApprovals = &n
		}
	}
	if rootKnown {
		var readme, docker bool
		for _, p := range root {
			n := strings.ToLower(strings.TrimPrefix(p, "/"))
			readme = readme || strings.HasPrefix(n, "readme")
			docker = docker || n == "dockerfile" || strings.HasSuffix(n, ".dockerfile")
		}
		f.HasReadme, f.HasDockerfile = boolp(readme), boolp(docker)
	}
	if commit != nil && commit.Author != nil {
		f.LastCommitter = commit.Author.Name
		f.LastCommitterEmail = services.EmailOf(commit.Author.Email)
		if t, err := time.Parse(time.RFC3339, commit.Author.Date); err == nil {
			f.LastCommitAt = &t
		}
	}
	return f
}
