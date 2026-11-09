package catalog

import (
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

// RepoKey is a repository's key on the hub: forge, owner, and name, lowercase.
func RepoKey(kind provider.Kind, fullName string) string {
	return strings.ToLower(string(kind) + "/" + fullName)
}

// ServiceKey is a service's key: its repository's, and its folder below it.
func ServiceKey(repoKey, id string) string {
	if id == "" {
		return repoKey
	}
	return repoKey + "/" + strings.ToLower(id)
}

func stamp(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func orNil[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

func ptr[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// staleAfter is how long an open pull request may sit before it counts as stale.
const staleAfter = 7 * 24 * time.Hour

// RepositoryEntity is one repository's facts. Its health is calculated in Orrery.
func RepositoryEntity(r *provider.Repo, now time.Time) sdk.Entity {
	stale, failing := 0, 0
	for _, pr := range r.PullRequests {
		if now.Sub(pr.UpdatedAt) > staleAfter {
			stale++
		}
		if !pr.Draft && pr.Checks == provider.ChecksFailure {
			failing++
		}
	}
	fields := map[string]any{
		"description": orNil(r.Description), "url": r.URL, "provider": string(r.Provider), "organization": r.AccountLogin, "name": r.Name,
		"language": orNil(services.ListedLanguage(r.Language)), "visibility": r.Visibility, "archived": r.Archived, "fork": r.Fork,
		"default_branch": r.DefaultBranch, "last_push": stamp(r.PushedAt), "open_prs": r.OpenPRs, "stale_prs": stale, "prs_failing_checks": failing,
		"open_issues": r.OpenIssues, "stars": r.Stars, "pipelines": len(r.Pipelines), "has_ci": len(r.Pipelines) > 0,
		"last_release": nil, "last_release_at": nil,
		"dependabot_critical": nil, "dependabot_high": nil, "code_scanning_critical": nil, "code_scanning_high": nil,
	}
	if len(r.Releases) > 0 {
		fields["last_release"], fields["last_release_at"] = r.Releases[0].Tag, stamp(&r.Releases[0].PublishedAt)
	}
	if a := r.Dependabot; a != nil {
		fields["dependabot_critical"], fields["dependabot_high"] = a.Critical, a.High
	}
	if a := r.CodeScanning; a != nil {
		fields["code_scanning_critical"], fields["code_scanning_high"] = a.Critical, a.High
	}
	if f := r.Facts; f != nil {
		fields["topics"] = f.Topics
		fields["branch_protected"] = ptr(f.BranchProtected)
		fields["requires_code_review"] = ptr(f.RequiresReview)
		fields["required_approvals"] = ptr(f.RequiredApprovals)
		fields["requires_code_owner_review"] = ptr(f.RequiresCodeOwnerReview)
		fields["has_codeowners"] = ptr(f.HasCodeowners)
		fields["has_readme"] = ptr(f.HasReadme)
		fields["has_dockerfile"] = ptr(f.HasDockerfile)
		fields["last_committer"] = orNil(f.LastCommitter)
		fields["last_committer_email"] = orNil(f.LastCommitterEmail)
		fields["last_commit_at"] = stamp(f.LastCommitAt)
	}
	return sdk.Entity{Blueprint: Repository, Key: RepoKey(r.Provider, r.FullName), Title: r.Name, Fields: fields}
}

// PipelineKey is a pipeline's key: its repository's, and the forge's id for it.
func PipelineKey(repoKey, id string) string { return repoKey + "/" + strings.ToLower(id) }

// PipelineEntity is one pipeline with what Judge worked out.
func PipelineEntity(r *provider.Repo, p provider.Pipeline, h Health) sdk.Entity {
	repo := RepoKey(r.Provider, r.FullName)
	var rate, p50 any
	if h.SuccessRate != nil {
		rate = *h.SuccessRate
	}
	if h.DurationP50 != nil {
		p50 = *h.DurationP50
	}
	var trend any
	if h.Trend != nil {
		trend = h.Trend
	}
	fields := map[string]any{
		"url": p.URL, "system": string(r.Provider), "name": p.Name, "path": p.Path, "default_branch": r.DefaultBranch, "state": p.State,
		"last_status": orNil(h.LastStatus), "last_run_at": stamp(h.LastRunAt), "last_run_url": orNil(h.LastRunURL),
		"default_branch_failing": h.Failing, "failing_runs": h.FailingRuns, "failing_since": stamp(h.FailingFrom),
		"schedule_state": h.Schedule, "schedule_missed_since": stamp(h.ScheduleMissedSince), "last_scheduled_run_at": stamp(h.LastScheduledRun),
		"release_ref": orNil(h.ReleaseRef), "release_build_failed": h.ReleaseFailed, "flaky": h.Flaky,
		"runs_30d": h.Runs30d, "success_rate_30d": rate, "duration_p50_seconds": p50, "trend_month": orNil(h.TrendMonth), "success_trend": trend,
	}
	return sdk.Entity{Blueprint: Pipeline, Key: PipelineKey(repo, p.ID), Title: r.FullName + " / " + p.Name, Fields: fields, Relations: map[string]any{"repository": repo}}
}

// languageIcons is the picture a service wears for its language, from the hub's icon set.
var languageIcons = map[string]string{
	"C#": "csharp", "Go": "go", "Python": "python", "TypeScript": "typescript", "JavaScript": "javascript", "Terraform": "terraform",
	"Bicep": "azure", "PowerShell": "powershell", "Shell": "bash", "Swift": "swift", "Rust": "rust", "C++": "cpp", "Lua": "lua",
	"HTML": "html", "CSS": "css", "SQL": "database",
}

// ServiceEntities are the services found in a repository. The one its
// orrery.yaml describes wears the logo it names (icon, once the hub holds it;
// empty keeps the hub's) and the APIs it calls, and is written even where no
// build file was found for it. The rest wear their language.
func ServiceEntities(r *provider.Repo, found *services.Found, icon string) []sdk.Entity {
	repo := RepoKey(r.Provider, r.FullName)
	d := found.Described
	var out []sdk.Entity
	described := false
	for _, s := range found.Services {
		key := ServiceKey(repo, s.ID)
		e := sdk.Entity{Blueprint: Service, Key: key, Title: s.Name, Icon: languageIcons[s.Language], Relations: map[string]any{"repository": repo}, Fields: map[string]any{
			"origin": "built", "path": s.Path, "type": s.Type, "language": orNil(s.Language),
			"last_changed_at": orNil(s.ChangedAt), "last_changed_by": orNil(s.ChangedBy), "last_changed_by_email": orNil(s.ChangedByEmail),
			"last_human_changed_at": orNil(s.HumanChangedAt), "last_human_changed_by": orNil(s.HumanChangedBy), "last_human_changed_by_email": orNil(s.HumanChangedByEmail),
			"has_readme": s.Files.Readme, "has_dockerfile": s.Files.Dockerfile, "has_tests": s.Files.Tests,
		}}
		if d != nil && d.Service == key {
			describe(&e, d, icon)
			described = true
		}
		out = append(out, e)
	}
	if d != nil && !described {
		e := sdk.Entity{Blueprint: Service, Key: d.Service, Title: d.Service[strings.LastIndex(d.Service, "/")+1:], Relations: map[string]any{"repository": repo}, Fields: map[string]any{"origin": "built"}}
		describe(&e, d, icon)
		out = append(out, e)
	}
	return out
}

// describe puts what an orrery.yaml says on its service: its logo and the
// APIs it calls. An empty list is written as one, so a connector removed
// from the file leaves the catalog.
func describe(e *sdk.Entity, d *services.Descriptor, icon string) {
	if icon != "" {
		e.Icon = icon
	} else if d.Icon != nil {
		e.Icon = ""
	}
	e.Relations["consumes"] = nonNil(d.Consumes)
	e.Relations["will_consume"] = nonNil(d.WillConsume)
}

// APIEntities are the APIs a repository's orrery.yaml says its service offers.
func APIEntities(r *provider.Repo, found *services.Found) []sdk.Entity {
	d := found.Described
	if d == nil {
		return nil
	}
	var out []sdk.Entity
	for _, a := range d.Provides {
		out = append(out, sdk.Entity{Blueprint: API, Key: d.Service + "/" + strings.ToLower(a.ID), Title: a.Title,
			Fields: map[string]any{"kind": a.Kind, "description": orNil(a.Description), "visibility": "internal", "external": false,
				"spec": services.SpecURL(a.Spec, r.URL, string(r.Provider))},
			Relations: map[string]any{"service": d.Service}})
	}
	return out
}

// Placement is a described service's place in the estate's own scheme: the
// fields and relations its orrery.yaml names, which redgreen does not define.
// Nil when it names none.
func Placement(found *services.Found) *sdk.Entity {
	d := found.Described
	if d == nil || len(d.Fields)+len(d.Relations) == 0 {
		return nil
	}
	return &sdk.Entity{Blueprint: Service, Key: d.Service, Fields: d.Fields, Relations: d.Relations}
}

// DeploymentService is the service a repository-wide deployment rolled out:
// the one at the root, or the repository's only one. "" when neither.
func DeploymentService(repoKey string, found *services.Found, folder string) string {
	if found == nil {
		return ""
	}
	for _, s := range found.Services {
		if s.Path == folder {
			return ServiceKey(repoKey, s.ID)
		}
	}
	if folder == "" && len(found.Services) == 1 {
		return ServiceKey(repoKey, found.Services[0].ID)
	}
	return ""
}

// Outcome is what redgreen computes about one deployment.
type Outcome struct {
	LeadTimeHours *float64
	CommitCount   *int
	FixOnly       *bool
	FailedChange  bool
	RestoreHours  *float64
}

// Fields are the computed fields, as written.
func (o Outcome) Fields() map[string]any {
	return map[string]any{"lead_time_hours": ptr(o.LeadTimeHours), "commit_count": ptr(o.CommitCount), "fix_only": ptr(o.FixOnly), "failed_change": o.FailedChange, "restore_hours": ptr(o.RestoreHours)}
}

// DeploymentEntity is one forge deployment that reached an end, with its computed fields.
func DeploymentEntity(repoKey, repoName, service string, d provider.Deployment, o Outcome) sdk.Entity {
	fields := map[string]any{"status": d.Status, "environment_name": d.Environment, "revision": d.SHA, "ref": orNil(d.Ref),
		"deployed_at": stamp(d.FinishedAt), "started_at": stamp(&d.CreatedAt), "initiated_by": orNil(d.Actor), "url": orNil(d.URL)}
	if d.FinishedAt != nil {
		if s := d.FinishedAt.Sub(d.CreatedAt).Seconds(); s >= 0 {
			fields["duration_seconds"] = int64(s + 0.5)
		}
	}
	for k, v := range o.Fields() {
		fields[k] = v
	}
	rel := map[string]any{"repository": repoKey}
	if service != "" {
		rel["service"] = service
	}
	return sdk.Entity{Blueprint: Deployment, Key: repoKey + "/" + d.ID, Title: repoName + " to " + d.Environment, Fields: fields, Relations: rel}
}

// IssueKey keys an issue under its repository; a pull request under pull/, since forges number the two apart.
func IssueKey(repoKey string, number int, pull bool) string {
	if pull {
		return repoKey + "/pull/" + strconv.Itoa(number)
	}
	return repoKey + "/" + strconv.Itoa(number)
}

// IssueEntities are a repository's open issues and pull requests.
func IssueEntities(r *provider.Repo) []sdk.Entity {
	repo := RepoKey(r.Provider, r.FullName)
	var out []sdk.Entity
	rel := map[string]any{"repository": repo}
	for _, i := range r.Issues {
		out = append(out, sdk.Entity{Blueprint: Issue, Key: IssueKey(repo, i.Number, false), Title: i.Title, Relations: rel, Fields: map[string]any{
			"kind": "issue", "state": "open", "number": i.Number, "url": i.URL, "repo": r.FullName, "author": orNil(i.Author),
			"labels": nonNil(i.Labels), "assignees": nonNil(i.Assignees), "comments": i.Comments, "milestone": orNil(i.Milestone), "issue_type": orNil(i.Type),
			"draft": false, "checks": nil, "created_at": stamp(&i.CreatedAt), "updated_at": stamp(&i.UpdatedAt), "closed_at": nil,
		}})
	}
	for _, p := range r.PullRequests {
		out = append(out, sdk.Entity{Blueprint: Issue, Key: IssueKey(repo, p.Number, true), Title: p.Title, Relations: rel, Fields: map[string]any{
			"kind": "pull_request", "state": "open", "number": p.Number, "url": p.URL, "repo": r.FullName, "author": orNil(p.Author),
			"labels": nonNil(p.Labels), "assignees": []string{}, "comments": nil, "milestone": nil, "issue_type": nil,
			"draft": p.Draft, "checks": orNil(p.Checks), "created_at": stamp(&p.CreatedAt), "updated_at": stamp(&p.UpdatedAt), "closed_at": nil,
		}})
	}
	return out
}

// Closed is an issue entity marked closed at t.
func Closed(e sdk.Entity, t time.Time) sdk.Entity {
	fields := map[string]any{}
	for k, v := range e.Fields {
		fields[k] = v
	}
	fields["state"], fields["closed_at"] = "closed", t.UTC().Format(time.RFC3339)
	e.Fields = fields
	return e
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
