package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
)

const (
	runsPerPipeline = 30
	deploymentsKept = 30
	mrChecksLimit   = 20
)

const securityQuery = `
query ($path: ID!) {
  project(fullPath: $path) {
    securityScanners { enabled }
    dependency: vulnerabilitySeveritiesCount(state: [DETECTED, CONFIRMED], reportType: [DEPENDENCY_SCANNING, CONTAINER_SCANNING]) { critical high }
    code: vulnerabilitySeveritiesCount(state: [DETECTED, CONFIRMED], reportType: [SAST, SECRET_DETECTION, DAST, API_FUZZING, COVERAGE_FUZZING]) { critical high }
  }
}`

// featureOff reads a feature a project may switch off, such as releases or
// merge requests. GitLab then answers 403 or 404, to an admin too: that is a
// project with none of them, not one that cannot be read.
func featureOff[T any](ctx context.Context, p *Provider, repo, feature string, none T, read func() (T, error)) (T, bool, error) {
	v, err := read()
	if err == nil {
		return v, true, nil
	}
	if provider.IsStatus(err, 403, 404) {
		p.log.DebugContext(ctx, "feature switched off for project; read as none", "repo", repo, "feature", feature, "error", err)
		return none, false, nil
	}
	return none, false, err
}

func (p *Provider) sync(ctx context.Context, a provider.Account, id int64) (*provider.Repo, error) {
	f := p.client()
	path := func(s string) string { return fmt.Sprintf("projects/%d%s", id, s) }
	project, err := one[glProject](ctx, f, p.api(path(""), nil))
	if err != nil {
		return nil, err
	}
	branch := project.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	last := project.LastActivityAt
	out := &provider.Repo{
		ID: "gitlab:" + strconv.FormatInt(project.ID, 10), Provider: provider.GitLab, AccountLogin: a.Login, Name: project.Name,
		FullName: project.PathWithNamespace, URL: project.WebURL, Description: project.Description, DefaultBranch: branch,
		Visibility: project.Visibility, Archived: project.Archived, Fork: project.ForkedFrom != nil, PushedAt: &last,
		Stars: project.StarCount, OpenIssues: project.OpenIssuesCount, SyncedAt: time.Now().UTC(),
	}
	name := project.PathWithNamespace
	if project.Archived {
		p.log.DebugContext(ctx, "archived project; skipping activity feeds", "repo", name)
		return out, nil
	}
	languages, err := one[map[string]float64](ctx, f, p.api(path("/languages"), nil))
	if err != nil {
		return nil, err
	}
	out.Language = services.MainLanguage(languages)
	tagList, err := one[[]struct {
		Name string `json:"name"`
	}](ctx, f, p.api(path("/repository/tags"), q("per_page", "30")))
	if err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	for _, t := range tagList {
		tags[t.Name] = true
	}
	releases, _, err := featureOff(ctx, p, name, "releases", []glRelease{}, func() ([]glRelease, error) {
		return one[[]glRelease](ctx, f, p.api(path("/releases"), q("per_page", "5")))
	})
	if err != nil {
		return nil, err
	}
	for _, r := range releases {
		n := r.Name
		if n == "" {
			n = r.TagName
		}
		out.Releases = append(out.Releases, provider.Release{Tag: r.TagName, Name: n, PublishedAt: r.ReleasedAt, URL: r.Links.Self, Prerelease: r.UpcomingRelease})
	}
	mrs, _, err := featureOff(ctx, p, name, "merge requests", []glMergeRequest{}, func() ([]glMergeRequest, error) {
		return one[[]glMergeRequest](ctx, f, p.api(path("/merge_requests"), q("state", "opened", "order_by", "updated_at", "sort", "desc", "per_page", "100")))
	})
	if err != nil {
		return nil, err
	}
	issues, issuesOn, err := featureOff(ctx, p, name, "issues", []glIssue{}, func() ([]glIssue, error) {
		return list[glIssue](ctx, f, p.api(path("/issues"), q("state", "opened", "per_page", "100")))
	})
	if err != nil {
		return nil, err
	}
	if issuesOn {
		out.Issues = []provider.Issue{}
		for _, i := range issues {
			out.Issues = append(out.Issues, issue(i))
		}
	}
	security, err := p.security(ctx, name)
	if err != nil {
		return nil, err
	}
	out.Dependabot, out.CodeScanning = securityCounts(security, project.WebURL)
	deployments, deploymentsOn, err := featureOff(ctx, p, name, "deployments", []glDeployment{}, func() ([]glDeployment, error) {
		return one[[]glDeployment](ctx, f, p.api(path("/deployments"), q("order_by", "created_at", "sort", "desc", "per_page", strconv.Itoa(deploymentsKept))))
	})
	if err != nil {
		return nil, err
	}
	if deploymentsOn {
		out.Deployments = []provider.Deployment{}
		for _, d := range deployments {
			out.Deployments = append(out.Deployments, deployment(d))
		}
	}
	if out.Facts, err = p.facts(ctx, project); err != nil {
		return nil, err
	}
	if out.Pipelines, err = p.pipelines(ctx, project, tags); err != nil {
		return nil, err
	}
	out.OpenPRs = len(mrs)
	for i, mr := range mrs {
		pr := provider.PullRequest{Number: mr.IID, Title: mr.Title, URL: mr.WebURL, Draft: mr.Draft, CreatedAt: mr.CreatedAt, UpdatedAt: mr.UpdatedAt, Labels: mr.Labels}
		if mr.Author != nil {
			pr.Author = mr.Author.Username
		}
		// The head pipeline is only on the single merge request view, so it costs one call per request.
		if i < mrChecksLimit {
			detail, err := one[struct {
				HeadPipeline *struct {
					Status string `json:"status"`
				} `json:"head_pipeline"`
			}](ctx, f, p.api(path(fmt.Sprintf("/merge_requests/%d", mr.IID)), nil))
			if err != nil {
				return nil, err
			}
			status := ""
			if detail.HeadPipeline != nil {
				status = detail.HeadPipeline.Status
			}
			pr.Checks = checksState(status)
		}
		out.PullRequests = append(out.PullRequests, pr)
	}
	return out, nil
}

// security reads vulnerability counts. They need Ultimate; on any other tier
// the query answers null or errors, and both feeds stay unavailable.
func (p *Provider) security(ctx context.Context, fullName string) (*glSecurity, error) {
	var res struct {
		Data *struct {
			Project *glSecurity `json:"project"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := p.client().Send(ctx, "POST", p.cfg.URL+"/api/graphql", map[string]any{"query": securityQuery, "variables": map[string]any{"path": fullName}}, &res)
	if err != nil {
		return nil, err
	}
	if len(res.Errors) > 0 || res.Data == nil || res.Data.Project == nil {
		p.log.DebugContext(ctx, "security counts unavailable", "repo", fullName, "errors", len(res.Errors))
		return nil, nil
	}
	return res.Data.Project, nil
}

// facts reads branch rules, the last commit, and what sits at the root.
// Approval rules are a paid-tier feature: a free-tier GitLab answers 404, and
// review rules stay unknown rather than reading as none required.
func (p *Provider) facts(ctx context.Context, project glProject) (*provider.Facts, error) {
	f := p.client()
	tolerate := func(err error) error {
		if provider.IsStatus(err, 403, 404) {
			return nil
		}
		return err
	}
	var branch *glBranch
	var protection *glProtectedBranch
	var root []string
	rootKnown := false
	if b := project.DefaultBranch; b != "" {
		got, err := one[glBranch](ctx, f, p.api(fmt.Sprintf("projects/%d/repository/branches/%s", project.ID, url.PathEscape(b)), nil))
		if err == nil {
			branch = &got
		} else if err = tolerate(err); err != nil {
			return nil, err
		}
		prot, err := one[glProtectedBranch](ctx, f, p.api(fmt.Sprintf("projects/%d/protected_branches/%s", project.ID, url.PathEscape(b)), nil))
		if err == nil {
			protection = &prot
		} else if err = tolerate(err); err != nil {
			return nil, err
		}
		items, err := one[[]struct {
			Name string `json:"name"`
		}](ctx, f, p.api(fmt.Sprintf("projects/%d/repository/tree", project.ID), q("ref", b, "per_page", "100")))
		if err == nil {
			rootKnown = true
			for _, i := range items {
				root = append(root, i.Name)
			}
		} else if err = tolerate(err); err != nil {
			return nil, err
		}
	}
	approvals, err := one[[]glApprovalRule](ctx, f, p.api(fmt.Sprintf("projects/%d/approval_rules", project.ID), nil))
	approvalsKnown := err == nil
	if err != nil {
		if err = tolerate(err); err != nil {
			return nil, err
		}
	}
	return facts(project, branch, protection, approvals, approvalsKnown, root, rootKnown), nil
}

func (p *Provider) pipelines(ctx context.Context, project glProject, tags map[string]bool) ([]provider.Pipeline, error) {
	f := p.client()
	id := project.ID
	recent, err := one[[]glPipeline](ctx, f, p.api(fmt.Sprintf("projects/%d/pipelines", id), q("per_page", "100")))
	if provider.IsStatus(err, 403, 404) {
		p.log.DebugContext(ctx, "ci unavailable", "repo", project.PathWithNamespace)
		return []provider.Pipeline{}, nil
	}
	if err != nil {
		return nil, err
	}
	schedules, err := one[[]glSchedule](ctx, f, p.api(fmt.Sprintf("projects/%d/pipeline_schedules", id), q("per_page", "100")))
	if provider.IsStatus(err, 403, 404) {
		return []provider.Pipeline{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(recent) == 0 && len(schedules) == 0 {
		return []provider.Pipeline{}, nil
	}
	// GitLab reports nothing like GitHub's dormant workflows: a project runs CI or has it switched off.
	ciDisabled := project.BuildsAccessLevel == "disabled"
	if ciDisabled {
		p.log.InfoContext(ctx, "ci switched off for project; pipelines marked disabled", "repo", project.PathWithNamespace)
	}
	branch := project.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	scheduled := func(x glPipeline) bool { return x.Source == "schedule" || x.Source == "scheduled" }
	onDefault := func(x glPipeline) bool {
		return x.Ref == branch && !scheduled(x) && x.Source != "merge_request_event" && x.Source != "external_pull_request_event"
	}
	runs := map[int64]glPipeline{}
	for _, x := range recent {
		if !scheduled(x) {
			runs[x.ID] = x
		}
	}
	decided := false
	for _, x := range runs {
		if onDefault(x) && (x.Status == "success" || x.Status == "failed") {
			decided = true
			break
		}
	}
	if !decided {
		more, err := one[[]glPipeline](ctx, f, p.api(fmt.Sprintf("projects/%d/pipelines", id), q("ref", branch, "per_page", "10")))
		if err != nil {
			return nil, err
		}
		for _, x := range more {
			if !scheduled(x) {
				runs[x.ID] = x
			}
		}
	}
	state := provider.Enabled
	if ciDisabled {
		state = provider.Disabled
	}
	out := []provider.Pipeline{mainPipeline(project, state, order(runs, tags))}
	for _, s := range schedules {
		fired, err := one[[]glPipeline](ctx, f, p.api(fmt.Sprintf("projects/%d/pipeline_schedules/%d/pipelines", id, s.ID), q("per_page", strconv.Itoa(runsPerPipeline), "sort", "desc")))
		if provider.IsStatus(err, 404) {
			// Older GitLab releases lack the endpoint; the schedule is judged from its own clock.
			p.log.InfoContext(ctx, "schedule runs unavailable on this gitlab; schedule shows no runs", "project", id, "schedule", s.ID)
			fired, err = nil, nil
		}
		if err != nil {
			return nil, err
		}
		m := map[int64]glPipeline{}
		for _, x := range fired {
			m[x.ID] = x
		}
		out = append(out, schedulePipeline(project, s, ciDisabled, order(m, tags)))
	}
	return out, nil
}

func order(m map[int64]glPipeline, tags map[string]bool) []provider.Run {
	list := make([]glPipeline, 0, len(m))
	for _, x := range m {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	if len(list) > runsPerPipeline {
		list = list[:runsPerPipeline]
	}
	out := []provider.Run{}
	for _, x := range list {
		out = append(out, run(x, tags))
	}
	return out
}
