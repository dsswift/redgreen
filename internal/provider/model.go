// Package provider is the neutral model every forge normalises into, and the
// interface a forge implements. Nothing past this package knows which forge a
// repository lives on.
package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// Kind names a forge.
type Kind string

const (
	GitHub      Kind = "github"
	GitLab      Kind = "gitlab"
	AzureDevOps Kind = "azuredevops"
)

// Account is an organisation, group, or user whose repositories are read.
type Account struct {
	ID       string `json:"id"`
	Provider Kind   `json:"provider"`
	Login    string `json:"login"`
	Name     string `json:"name"`
	Org      bool   `json:"org"`
	URL      string `json:"url"`
	// Conn is what the forge needs to act as this account, such as a GitHub App installation id.
	Conn string `json:"conn,omitempty"`
}

// RepoRef is a repository found under an account.
type RepoRef struct {
	ID       string `json:"id"`
	FullName string `json:"fullName"`
}

// Run triggers.
const (
	TriggerPush     = "push"
	TriggerPR       = "pull_request"
	TriggerSchedule = "schedule"
	TriggerRelease  = "release"
	TriggerTag      = "tag"
	TriggerManual   = "manual"
	TriggerOther    = "other"
)

// Run states and conclusions.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"

	Success        = "success"
	Failure        = "failure"
	Cancelled      = "cancelled"
	Skipped        = "skipped"
	Neutral        = "neutral"
	TimedOut       = "timed_out"
	ActionRequired = "action_required"
	StartupFailure = "startup_failure"
	Stale          = "stale"
)

// Run is one pipeline run.
type Run struct {
	ID         string    `json:"id"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Trigger    string    `json:"trigger"`
	Ref        string    `json:"ref"`
	SHA        string    `json:"sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	URL        string    `json:"url"`
	Actor      string    `json:"actor,omitempty"`
}

// Pipeline states.
const (
	Enabled  = "enabled"
	Disabled = "disabled"
	// Dormant is switched off by the forge after a stretch without activity.
	Dormant = "dormant"
)

// Schedule is one cron a pipeline fires on, evaluated in TimeZone.
type Schedule struct {
	Cron     string `json:"cron"`
	TimeZone string `json:"timeZone"`
}

// Pipeline is a workflow, a build definition, or a schedule.
type Pipeline struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	URL       string     `json:"url"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Schedules []Schedule `json:"schedules"`
	// Runs are the most recent, newest first.
	Runs []Run `json:"runs"`
}

// Release is one published release.
type Release struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"publishedAt"`
	URL         string    `json:"url"`
	Prerelease  bool      `json:"prerelease"`
}

// Deployment statuses.
const (
	DeploySucceeded = "succeeded"
	DeployFailed    = "failed"
	DeployRunning   = "running"
	DeployOther     = "other"
)

// Deployment is one rollout a forge recorded against an environment.
type Deployment struct {
	ID          string     `json:"id"`
	Environment string     `json:"environment"`
	Status      string     `json:"status"`
	Ref         string     `json:"ref,omitempty"`
	SHA         string     `json:"sha"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	URL         string     `json:"url,omitempty"`
	Actor       string     `json:"actor,omitempty"`
}

// Checks states of a pull request.
const (
	ChecksSuccess = "success"
	ChecksFailure = "failure"
	ChecksPending = "pending"
	ChecksNone    = "none"
)

// PullRequest is an open pull or merge request. Checks is empty when not read.
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Author    string    `json:"author,omitempty"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Checks    string    `json:"checks,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
}

// Issue is an open issue.
type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Author    string    `json:"author,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
	Assignees []string  `json:"assignees,omitempty"`
	Comments  int       `json:"comments"`
	Milestone string    `json:"milestone,omitempty"`
	Type      string    `json:"type,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Alerts counts open security alerts of one kind.
type Alerts struct {
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	URL      string `json:"url"`
}

// Facts are what a repository says about itself. A nil pointer is unknown.
type Facts struct {
	Topics                  []string   `json:"topics"`
	BranchProtected         *bool      `json:"branchProtected"`
	RequiresReview          *bool      `json:"requiresReview"`
	RequiredApprovals       *int       `json:"requiredApprovals"`
	RequiresCodeOwnerReview *bool      `json:"requiresCodeOwnerReview"`
	HasCodeowners           *bool      `json:"hasCodeowners"`
	HasReadme               *bool      `json:"hasReadme"`
	HasDockerfile           *bool      `json:"hasDockerfile"`
	LastCommitter           string     `json:"lastCommitter,omitempty"`
	LastCommitterEmail      string     `json:"lastCommitterEmail,omitempty"`
	LastCommitAt            *time.Time `json:"lastCommitAt,omitempty"`
}

// Repo is one repository as one sync read it.
type Repo struct {
	ID            string        `json:"id"`
	Provider      Kind          `json:"provider"`
	AccountLogin  string        `json:"accountLogin"`
	Name          string        `json:"name"`
	FullName      string        `json:"fullName"`
	URL           string        `json:"url"`
	Description   string        `json:"description,omitempty"`
	DefaultBranch string        `json:"defaultBranch"`
	Visibility    string        `json:"visibility"`
	Archived      bool          `json:"archived"`
	Fork          bool          `json:"fork"`
	Language      string        `json:"language,omitempty"`
	PushedAt      *time.Time    `json:"pushedAt,omitempty"`
	Stars         int           `json:"stars"`
	OpenIssues    int           `json:"openIssues"`
	OpenPRs       int           `json:"openPullRequests"`
	Pipelines     []Pipeline    `json:"pipelines"`
	Releases      []Release     `json:"releases"`
	PullRequests  []PullRequest `json:"pullRequests"`
	// Issues is nil when the forge keeps no issues for the repository.
	Issues       []Issue `json:"issues"`
	Dependabot   *Alerts `json:"dependabot,omitempty"`
	CodeScanning *Alerts `json:"codeScanning,omitempty"`
	Facts        *Facts  `json:"facts,omitempty"`
	// Deployments is nil when the forge keeps none for the repository or will not show them.
	Deployments []Deployment `json:"deployments"`
	SyncedAt    time.Time    `json:"syncedAt"`
}

// Commit is one commit a deployment carried.
type Commit struct {
	SHA        string    `json:"sha"`
	AuthoredAt time.Time `json:"authoredAt"`
	Title      string    `json:"title"`
}

// Delivery is what a webhook asks for.
type Delivery struct {
	ID    string
	Event string
	// Repos are the full names to read again.
	Repos []string
	// Rediscover is set when accounts or repository lists may have changed.
	Rediscover bool
	// Job is set when the delivery reports a CI job changing state.
	Job *Job
}

// Job states.
const (
	JobQueued     = "queued"
	JobInProgress = "in_progress"
	JobCompleted  = "completed"
)

// Runner kinds: the forge's own machines, or runners the estate hosts.
const (
	RunnerHosted     = "hosted"
	RunnerSelfHosted = "self-hosted"
)

// Job is one CI job as a webhook reported it.
type Job struct {
	ID   string
	Repo string
	// Status is JobQueued, JobInProgress, or JobCompleted.
	Status     string
	Conclusion string
	// Runner is RunnerHosted or RunnerSelfHosted: the runner that took the
	// job, or before one does, what the job asked for.
	Runner string
	// OS is linux, macos, or windows.
	OS          string
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// Provider is a forge. Everything past it is neutral.
type Provider interface {
	Kind() Kind
	Configured() bool
	// Accounts lists the accounts allowed to sync.
	Accounts(ctx context.Context) ([]Account, error)
	Repos(ctx context.Context, a Account) ([]RepoRef, error)
	Sync(ctx context.Context, a Account, r RepoRef) (*Repo, error)
	// Files is the repository's files at the head of its default branch, nil for an empty one.
	Files(ctx context.Context, a Account, r *Repo) (services.Files, error)
	// CommitsBetween lists the commits after base up to head, oldest first; head alone when base is empty.
	// Nil with no error when the forge cannot say.
	CommitsBetween(ctx context.Context, a Account, r *Repo, base, head string) ([]Commit, error)
	Quotas() []telemetry.Quota
}

// Webhooks is a forge that sends webhooks.
type Webhooks interface {
	// Webhook verifies a delivery and says what to read again.
	Webhook(ctx context.Context, header http.Header, body []byte) (Delivery, error)
}
