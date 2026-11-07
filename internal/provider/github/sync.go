package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

const (
	// runsPerPipeline is enough to see a failing streak.
	runsPerPipeline = 30
	// deploymentsKept is how many deployments have their statuses read, one call each.
	deploymentsKept = 15
	// prChecksLimit is how many open pull requests have their checks read.
	prChecksLimit = 20
)

type repoAPI struct {
	f    *provider.Fetcher
	base string
	log  *slog.Logger
}

func (r repoAPI) url(path string, q url.Values) string {
	s := r.base + path
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// tolerate answers ok=false when the forge says the feed is not there or not readable.
func tolerate[T any](r repoAPI, ctx context.Context, what string, read func() (T, error), codes ...int) (T, bool, error) {
	v, err := read()
	if err == nil {
		return v, true, nil
	}
	if provider.IsStatus(err, codes...) {
		r.log.DebugContext(ctx, "feed unavailable", "repo", r.base, "what", what, "error", err)
		var zero T
		return zero, false, nil
	}
	var zero T
	return zero, false, err
}

func syncRepo(ctx context.Context, api string, f *provider.Fetcher, account provider.Account, fullName string, log *slog.Logger) (*provider.Repo, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" {
		return nil, fmt.Errorf("not an owner/name pair: %s", fullName)
	}
	r := repoAPI{f: f, base: api + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), log: log}
	gh, err := one[ghRepo](ctx, f, r.url("", nil))
	if err != nil {
		return nil, err
	}
	out := &provider.Repo{
		ID: "github:" + itoa(gh.ID), Provider: provider.GitHub, AccountLogin: account.Login, Name: gh.Name, FullName: gh.FullName, URL: gh.HTMLURL,
		Description: gh.Description, DefaultBranch: gh.DefaultBranch, Visibility: visibility(gh), Archived: gh.Archived, Fork: gh.Fork,
		Language: gh.Language, PushedAt: gh.PushedAt, Stars: gh.Stars, OpenIssues: gh.OpenIssues, SyncedAt: time.Now().UTC(),
	}
	if gh.Archived {
		log.DebugContext(ctx, "archived repo; skipping activity feeds", "repo", fullName)
		return out, nil
	}
	actions, actionsKnown, err := tolerate(r, ctx, "actions setting", func() (bool, error) {
		p, err := one[struct {
			Enabled bool `json:"enabled"`
		}](ctx, f, r.url("/actions/permissions", nil))
		return p.Enabled, err
	}, 403, 404)
	if err != nil {
		return nil, err
	}
	tags, err := fetchTags(ctx, r)
	if err != nil {
		return nil, err
	}
	if out.Releases, err = fetchReleases(ctx, r); err != nil {
		return nil, err
	}
	pulls, err := one[[]ghPull](ctx, f, r.url("/pulls", q("state", "open", "sort", "updated", "direction", "desc", "per_page", "100")))
	if err != nil {
		return nil, err
	}
	if out.Dependabot, err = fetchDependabot(ctx, r, gh.HTMLURL); err != nil {
		return nil, err
	}
	if out.CodeScanning, err = fetchCodeScanning(ctx, r, gh.HTMLURL); err != nil {
		return nil, err
	}
	if out.Facts, err = fetchFacts(ctx, r, gh); err != nil {
		return nil, err
	}
	if out.Deployments, err = fetchDeployments(ctx, r); err != nil {
		return nil, err
	}
	issues, err := list[ghIssue](ctx, f, r.url("/issues", q("state", "open", "per_page", "100")))
	if err != nil {
		return nil, err
	}
	out.Issues = []provider.Issue{}
	for _, i := range issues {
		if i.PullRequest == nil {
			out.Issues = append(out.Issues, issue(i))
		}
	}
	listed, err := fetchPipelines(ctx, r, gh.DefaultBranch, tags)
	if err != nil {
		return nil, err
	}
	// Workflows keep reporting "active" after Actions is switched off for the whole repository.
	if actionsKnown && !actions {
		for i := range listed {
			listed[i].State = provider.Disabled
		}
		if len(listed) > 0 {
			log.InfoContext(ctx, "actions switched off for repo; pipelines marked disabled", "repo", fullName, "pipelines", len(listed))
		}
	}
	out.Pipelines = listed
	if out.PullRequests, err = withChecks(ctx, r, pulls); err != nil {
		return nil, err
	}
	out.OpenPRs = len(pulls)
	out.OpenIssues = max(0, gh.OpenIssues-len(pulls))
	return out, nil
}

func fetchTags(ctx context.Context, r repoAPI) (map[string]bool, error) {
	tags, err := one[[]struct {
		Name string `json:"name"`
	}](ctx, r.f, r.url("/tags", q("per_page", "30")))
	out := map[string]bool{}
	for _, t := range tags {
		out[t.Name] = true
	}
	return out, err
}

func fetchReleases(ctx context.Context, r repoAPI) ([]provider.Release, error) {
	releases, err := one[[]ghRelease](ctx, r.f, r.url("/releases", q("per_page", "5")))
	if err != nil {
		return nil, err
	}
	var out []provider.Release
	for _, rel := range releases {
		if rel.Draft || rel.PublishedAt == nil {
			continue
		}
		name := rel.Name
		if name == "" {
			name = rel.TagName
		}
		out = append(out, provider.Release{Tag: rel.TagName, Name: name, PublishedAt: *rel.PublishedAt, URL: rel.HTMLURL, Prerelease: rel.Prerelease})
	}
	return out, nil
}

// fetchDeployments reads the deployments a workflow job recorded against an
// environment. The list carries no outcome, so each one's statuses are read too.
func fetchDeployments(ctx context.Context, r repoAPI) ([]provider.Deployment, error) {
	list, ok, err := tolerate(r, ctx, "deployments", func() ([]ghDeployment, error) {
		return one[[]ghDeployment](ctx, r.f, r.url("/deployments", q("per_page", fmt.Sprint(deploymentsKept))))
	}, 403, 404)
	if err != nil || !ok {
		return nil, err
	}
	out := []provider.Deployment{}
	for _, d := range list {
		statuses, err := one[[]ghDeploymentStatus](ctx, r.f, r.url("/deployments/"+itoa(d.ID)+"/statuses", q("per_page", "10")))
		if err != nil {
			return nil, err
		}
		out = append(out, deployment(d, statuses))
	}
	return out, nil
}

func withChecks(ctx context.Context, r repoAPI, pulls []ghPull) ([]provider.PullRequest, error) {
	out := []provider.PullRequest{}
	for i, pr := range pulls {
		p := provider.PullRequest{Number: pr.Number, Title: pr.Title, URL: pr.HTMLURL, Draft: pr.Draft, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt, Labels: names(pr.Labels)}
		if pr.User != nil {
			p.Author = pr.User.Login
		}
		if i < prChecksLimit {
			ref := url.PathEscape(pr.Head.SHA)
			checks, err := one[struct {
				CheckRuns []ghCheckRun `json:"check_runs"`
			}](ctx, r.f, r.url("/commits/"+ref+"/check-runs", q("per_page", "100")))
			if err != nil {
				return nil, err
			}
			status, err := one[ghCombinedStatus](ctx, r.f, r.url("/commits/"+ref+"/status", nil))
			if err != nil {
				return nil, err
			}
			p.Checks = checksState(checks.CheckRuns, status)
		}
		out = append(out, p)
	}
	return out, nil
}

func fetchDependabot(ctx context.Context, r repoAPI, repoURL string) (*provider.Alerts, error) {
	alerts, ok, err := tolerate(r, ctx, "dependabot", func() ([]ghDependabotAlert, error) {
		return list[ghDependabotAlert](ctx, r.f, r.url("/dependabot/alerts", q("state", "open", "severity", "critical,high", "per_page", "100")))
	}, 403, 404)
	if err != nil || !ok {
		return nil, err
	}
	return dependabotCounts(alerts, repoURL+"/security/dependabot"), nil
}

func fetchCodeScanning(ctx context.Context, r repoAPI, repoURL string) (*provider.Alerts, error) {
	alerts, ok, err := tolerate(r, ctx, "code scanning", func() ([]ghCodeScanningAlert, error) {
		return list[ghCodeScanningAlert](ctx, r.f, r.url("/code-scanning/alerts", q("state", "open", "per_page", "100")))
	}, 403, 404)
	if err != nil || !ok {
		return nil, err
	}
	return codeScanningCounts(alerts, repoURL+"/security/code-scanning"), nil
}

type ghBranch struct {
	Protected bool     `json:"protected"`
	Commit    ghCommit `json:"commit"`
}

type ghRule struct {
	Type       string `json:"type"`
	Parameters *struct {
		RequiredApprovals int  `json:"required_approving_review_count"`
		CodeOwnerReview   bool `json:"require_code_owner_review"`
	} `json:"parameters"`
}

type ghProtection struct {
	Reviews *struct {
		RequiredApprovals int  `json:"required_approving_review_count"`
		CodeOwnerReviews  bool `json:"require_code_owner_reviews"`
	} `json:"required_pull_request_reviews"`
}

type ghEntry struct {
	Name string `json:"name"`
}

func boolp(b bool) *bool { return &b }

// fetchFacts reads branch rules, the last commit, and which well-known files
// exist. Each part degrades to unknown when unreadable.
func fetchFacts(ctx context.Context, r repoAPI, gh ghRepo) (*provider.Facts, error) {
	facts := &provider.Facts{Topics: gh.Topics}
	if facts.Topics == nil {
		facts.Topics = []string{}
	}
	branchPath := "/branches/" + url.PathEscape(gh.DefaultBranch)
	branch, hasBranch, err := tolerate(r, ctx, "branch", func() (ghBranch, error) { return one[ghBranch](ctx, r.f, r.url(branchPath, nil)) }, 403, 404, 409)
	if err != nil {
		return nil, err
	}
	rules, hasRules, err := tolerate(r, ctx, "rules", func() ([]ghRule, error) {
		return one[[]ghRule](ctx, r.f, r.url("/rules"+branchPath, q("per_page", "100")))
	}, 403, 404, 409)
	if err != nil {
		return nil, err
	}
	classic, hasClassic, err := tolerate(r, ctx, "protection", func() (ghProtection, error) {
		return one[ghProtection](ctx, r.f, r.url(branchPath+"/protection", nil))
	}, 403, 404, 409)
	if err != nil {
		return nil, err
	}
	listing := func(path string) ([]ghEntry, bool, error) {
		return tolerate(r, ctx, "contents "+path, func() ([]ghEntry, error) {
			return provider.Get(ctx, r.f, r.url("/contents/"+path, nil), func(a provider.Answer) ([]ghEntry, error) {
				var entries []ghEntry
				if json.Unmarshal(a.Body, &entries) != nil {
					return nil, nil
				}
				return entries, nil
			})
		}, 403, 404, 409)
	}
	root, hasRoot, err := listing("")
	if err != nil {
		return nil, err
	}
	dotGitHub, _, err := listing(".github")
	if err != nil {
		return nil, err
	}
	if hasBranch {
		facts.BranchProtected = boolp(branch.Protected)
		if branch.Commit.Author != nil && branch.Commit.Author.Login != "" {
			facts.LastCommitter = branch.Commit.Author.Login
		}
		if a := branch.Commit.Commit.Author; a != nil {
			if facts.LastCommitter == "" {
				facts.LastCommitter = a.Name
			}
			facts.LastCommitterEmail = services.EmailOf(a.Email)
			if t, err := time.Parse(time.RFC3339, a.Date); err == nil {
				facts.LastCommitAt = &t
			}
		}
	}
	if hasRules {
		if len(rules) > 0 {
			facts.BranchProtected = boolp(true)
		}
		facts.RequiresReview = boolp(false)
		for _, rule := range rules {
			if rule.Type != "pull_request" {
				continue
			}
			facts.RequiresReview = boolp(true)
			n, owners := 0, false
			if rule.Parameters != nil {
				n, owners = rule.Parameters.RequiredApprovals, rule.Parameters.CodeOwnerReview
			}
			facts.RequiredApprovals = &n
			facts.RequiresCodeOwnerReview = boolp(owners)
			break
		}
	}
	if hasClassic && classic.Reviews != nil {
		facts.RequiresReview = boolp(true)
		n := classic.Reviews.RequiredApprovals
		if facts.RequiredApprovals != nil && *facts.RequiredApprovals > n {
			n = *facts.RequiredApprovals
		}
		facts.RequiredApprovals = &n
		facts.RequiresCodeOwnerReview = boolp((facts.RequiresCodeOwnerReview != nil && *facts.RequiresCodeOwnerReview) || classic.Reviews.CodeOwnerReviews)
	}
	if hasRoot && root != nil {
		var readme, docker, owners bool
		for _, e := range root {
			n := strings.ToLower(e.Name)
			readme = readme || strings.HasPrefix(n, "readme")
			docker = docker || n == "dockerfile" || strings.HasSuffix(n, ".dockerfile")
			owners = owners || n == "codeowners"
		}
		for _, e := range dotGitHub {
			owners = owners || strings.ToLower(e.Name) == "codeowners"
		}
		facts.HasReadme, facts.HasDockerfile, facts.HasCodeowners = boolp(readme), boolp(docker), boolp(owners)
	}
	return facts, nil
}

func fetchPipelines(ctx context.Context, r repoAPI, defaultBranch string, tags map[string]bool) ([]provider.Pipeline, error) {
	workflows, ok, err := tolerate(r, ctx, "actions", func() ([]ghWorkflow, error) {
		return listOf(ctx, r.f, r.url("/actions/workflows", q("per_page", "100")), func(b []byte) ([]ghWorkflow, error) {
			var p struct {
				Workflows []ghWorkflow `json:"workflows"`
			}
			err := json.Unmarshal(b, &p)
			return p.Workflows, err
		})
	}, 403, 404)
	if err != nil || !ok {
		return []provider.Pipeline{}, err
	}
	type live struct {
		w     ghWorkflow
		state string
	}
	var alive []live
	for _, w := range workflows {
		if s, ok := pipelineState(w.State); ok {
			alive = append(alive, live{w, s})
		}
	}
	if len(alive) == 0 {
		return []provider.Pipeline{}, nil
	}
	// One page of recent runs covers most pipelines; the gaps are filled per pipeline below.
	recent, err := one[struct {
		Runs []ghRun `json:"workflow_runs"`
	}](ctx, r.f, r.url("/actions/runs", q("per_page", "100")))
	if err != nil {
		return nil, err
	}
	out := []provider.Pipeline{}
	for _, l := range alive {
		schedules, err := fetchSchedules(ctx, r, l.w, defaultBranch)
		if err != nil {
			return nil, err
		}
		runs := map[int64]ghRun{}
		for _, run := range recent.Runs {
			if run.WorkflowID == l.w.ID {
				runs[run.ID] = run
			}
		}
		onDefault := func(x ghRun) bool {
			return str(x.HeadBranch) == defaultBranch && x.Event != "pull_request" && x.Event != "pull_request_target"
		}
		decisive := func(x ghRun) bool {
			return str(x.Status) == "completed" && str(x.Conclusion) != "cancelled" && str(x.Conclusion) != "skipped"
		}
		if !anyRun(runs, func(x ghRun) bool { return onDefault(x) && decisive(x) }) {
			more, err := workflowRuns(ctx, r, l.w.ID, q("branch", defaultBranch, "exclude_pull_requests", "true", "per_page", "10"))
			if err != nil {
				return nil, err
			}
			for _, x := range more {
				runs[x.ID] = x
			}
		}
		if len(schedules) > 0 && !anyRun(runs, func(x ghRun) bool { return x.Event == "schedule" }) {
			more, err := workflowRuns(ctx, r, l.w.ID, q("event", "schedule", "per_page", "1"))
			if err != nil {
				return nil, err
			}
			for _, x := range more {
				runs[x.ID] = x
			}
		}
		ordered := make([]ghRun, 0, len(runs))
		for _, x := range runs {
			ordered = append(ordered, x)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].CreatedAt.After(ordered[j].CreatedAt) })
		if len(ordered) > runsPerPipeline {
			ordered = ordered[:runsPerPipeline]
		}
		p := provider.Pipeline{ID: itoa(l.w.ID), Name: l.w.Name, Path: l.w.Path, URL: l.w.HTMLURL, State: l.state, CreatedAt: l.w.CreatedAt, UpdatedAt: l.w.UpdatedAt, Schedules: schedules, Runs: []provider.Run{}}
		for _, x := range ordered {
			p.Runs = append(p.Runs, run(x, tags))
		}
		out = append(out, p)
	}
	return out, nil
}

func anyRun(runs map[int64]ghRun, ok func(ghRun) bool) bool {
	for _, r := range runs {
		if ok(r) {
			return true
		}
	}
	return false
}

func workflowRuns(ctx context.Context, r repoAPI, workflow int64, query url.Values) ([]ghRun, error) {
	p, err := one[struct {
		Runs []ghRun `json:"workflow_runs"`
	}](ctx, r.f, r.url("/actions/workflows/"+itoa(workflow)+"/runs", query))
	return p.Runs, err
}

// fetchSchedules reads the schedules a workflow file declares. Dynamic
// workflows, such as one from a marketplace app, have no file in the repository.
func fetchSchedules(ctx context.Context, r repoAPI, w ghWorkflow, ref string) ([]provider.Schedule, error) {
	if !strings.HasPrefix(w.Path, ".github/workflows/") {
		return nil, nil
	}
	schedules, _, err := tolerate(r, ctx, "workflow file", func() ([]provider.Schedule, error) {
		return provider.Get(ctx, r.f, r.url("/contents/"+w.Path, q("ref", ref)), func(a provider.Answer) ([]provider.Schedule, error) {
			var file struct {
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := json.Unmarshal(a.Body, &file); err != nil || file.Encoding != "base64" || file.Content == "" {
				return []provider.Schedule{}, nil
			}
			text, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
			if err != nil {
				return []provider.Schedule{}, nil
			}
			return ParseSchedules(string(text)), nil
		})
	}, 404)
	return schedules, err
}
