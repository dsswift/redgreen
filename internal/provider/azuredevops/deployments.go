package azuredevops

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

const (
	// deploymentsTTL is how long one read of a project's deployments answers for its repositories.
	deploymentsTTL = 5 * time.Minute
	// deploymentsWindow is how far back an environment's deployments are read.
	deploymentsWindow = 200 * 24 * time.Hour
	recordsPerPage    = 500
	buildsPerCall     = 100
	// compareLimit is the most commits a compare lists; a longer range is read in part and says so.
	compareLimit = 250
)

type adoEnvironment struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// adoDeploymentRecord is one job a pipeline ran against an environment.
// Its owner is the build the job ran in.
type adoDeploymentRecord struct {
	ID         int64      `json:"id"`
	Result     string     `json:"result"`
	QueueTime  time.Time  `json:"queueTime"`
	StartTime  *time.Time `json:"startTime"`
	FinishTime *time.Time `json:"finishTime"`
	Owner      struct {
		ID    int64 `json:"id"`
		Links *struct {
			Web *struct {
				Href string `json:"href"`
			} `json:"web"`
		} `json:"_links"`
	} `json:"owner"`
}

// deployedBuild is what a deployment takes from its build. gone marks a build
// the forge no longer keeps.
type deployedBuild struct {
	repo, sha, ref, actor string
	gone                  bool
}

// projectDeployments is what one project's environments recorded, by
// repository. mu lets one read at a time fill it.
type projectDeployments struct {
	mu     sync.Mutex
	at     time.Time
	shown  bool
	byRepo map[string][]provider.Deployment
	// builds outlive a read: a build's repository and commit never change.
	builds map[int64]deployedBuild
}

// deployments are the deployments a repository's builds made to its project's
// environments, newest first, or nil when the project shows no environment.
// The forge files them by environment, so one read covers a whole project.
func (p *Provider) deployments(ctx context.Context, f *provider.Fetcher, s scope) ([]provider.Deployment, error) {
	key := s.org + "/" + s.repo.Project.ID
	p.mu.Lock()
	e := p.deployed[key]
	if e == nil {
		e = &projectDeployments{builds: map[int64]deployedBuild{}}
		p.deployed[key] = e
	}
	p.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.at.IsZero() || time.Since(e.at) >= deploymentsTTL {
		if err := p.readDeployments(ctx, f, s, e); err != nil {
			return nil, err
		}
		e.at = time.Now()
	}
	if !e.shown {
		return nil, nil
	}
	if found := e.byRepo[strings.ToLower(s.repo.ID)]; found != nil {
		return found, nil
	}
	return []provider.Deployment{}, nil
}

// readDeployments reads every environment's records back to the window and
// the builds they name, and files each deployment under its build's repository.
func (p *Provider) readDeployments(ctx context.Context, f *provider.Fetcher, s scope, e *projectDeployments) error {
	unavailable := func(err error) bool { return provider.IsStatus(err, 401, 403, 404) }
	envs, err := list[adoEnvironment](ctx, f, s.project+"/_apis/distributedtask/environments", nil)
	if unavailable(err) || (err == nil && len(envs) == 0) {
		p.log.DebugContext(ctx, "no environments shown; deployments not read", "org", s.org, "project", s.repo.Project.Name)
		e.shown, e.byRepo = false, nil
		return nil
	}
	if err != nil {
		return err
	}
	since := time.Now().Add(-deploymentsWindow)
	older := func(got []adoDeploymentRecord) bool { return len(got) > 0 && got[len(got)-1].QueueTime.Before(since) }
	type placed struct {
		record      adoDeploymentRecord
		environment string
	}
	var records []placed
	need := map[int64]bool{}
	for _, env := range envs {
		u := fmt.Sprintf("%s/_apis/distributedtask/environments/%d/environmentdeploymentrecords", s.project, env.ID)
		found, err := collect[adoDeploymentRecord](ctx, f, u, q("top", strconv.Itoa(recordsPerPage)), provider.Get[page[adoDeploymentRecord]], older)
		if unavailable(err) {
			p.log.InfoContext(ctx, "environment's deployments not shown; left out", "org", s.org, "project", s.repo.Project.Name, "environment", env.Name)
			continue
		}
		if err != nil {
			return err
		}
		for _, r := range found {
			if r.QueueTime.Before(since) {
				continue
			}
			records = append(records, placed{r, env.Name})
			need[r.Owner.ID] = true
		}
	}
	if err := p.deployedBuilds(ctx, f, s, e, need); err != nil {
		return err
	}
	byRepo := map[string][]provider.Deployment{}
	gone := 0
	for _, r := range records {
		b := e.builds[r.record.Owner.ID]
		if b.gone {
			gone++
			continue
		}
		byRepo[b.repo] = append(byRepo[b.repo], deployment(r.record, r.environment, b))
	}
	for _, found := range byRepo {
		sort.SliceStable(found, func(i, j int) bool { return found[i].CreatedAt.After(found[j].CreatedAt) })
	}
	e.shown, e.byRepo = true, byRepo
	p.log.InfoContext(ctx, "azure devops deployments read", "org", s.org, "project", s.repo.Project.Name, "environments", len(envs), "records", len(records), "kept", len(records)-gone, "buildGone", gone, "repositories", len(byRepo))
	return nil
}

// deployedBuilds reads the builds in need that are not held yet, and forgets
// the ones no record names any more.
func (p *Provider) deployedBuilds(ctx context.Context, f *provider.Fetcher, s scope, e *projectDeployments, need map[int64]bool) error {
	var ask []int64
	for id := range need {
		if _, ok := e.builds[id]; !ok {
			ask = append(ask, id)
		}
	}
	sort.Slice(ask, func(i, j int) bool { return ask[i] < ask[j] })
	for len(ask) > 0 {
		batch := ask[:min(buildsPerCall, len(ask))]
		ask = ask[len(batch):]
		ids := make([]string, len(batch))
		for i, id := range batch {
			ids[i] = strconv.FormatInt(id, 10)
		}
		got, err := once[struct {
			Value []adoBuild `json:"value"`
		}](ctx, f, s.project+"/_apis/build/builds", q("buildIds", strings.Join(ids, ",")))
		if err != nil {
			return err
		}
		for _, b := range got.Value {
			d := deployedBuild{sha: b.SourceVersion, ref: shortRef(b.SourceBranch)}
			if b.Repository != nil {
				d.repo = strings.ToLower(b.Repository.ID)
			}
			if b.RequestedFor != nil {
				d.actor = b.RequestedFor.DisplayName
			}
			e.builds[b.ID] = d
		}
		for _, id := range batch {
			if _, ok := e.builds[id]; !ok {
				e.builds[id] = deployedBuild{gone: true}
			}
		}
	}
	for id := range e.builds {
		if !need[id] {
			delete(e.builds, id)
		}
	}
	return nil
}

// deployStatus maps a job's result. A job with warnings still deployed.
func deployStatus(r adoDeploymentRecord) string {
	switch r.Result {
	case "succeeded", "succeededWithIssues":
		return provider.DeploySucceeded
	case "failed":
		return provider.DeployFailed
	case "":
		if r.FinishTime == nil {
			return provider.DeployRunning
		}
	}
	return provider.DeployOther
}

func deployment(r adoDeploymentRecord, environment string, b deployedBuild) provider.Deployment {
	d := provider.Deployment{ID: strconv.FormatInt(r.ID, 10), Environment: environment, Status: deployStatus(r), Ref: b.ref, SHA: b.sha, CreatedAt: r.QueueTime, Actor: b.actor}
	if r.StartTime != nil {
		d.CreatedAt = *r.StartTime
	}
	if d.Status == provider.DeploySucceeded || d.Status == provider.DeployFailed {
		d.FinishedAt = r.FinishTime
	}
	if r.Owner.Links != nil && r.Owner.Links.Web != nil {
		d.URL = r.Owner.Links.Web.Href
	}
	return d
}

// CommitsBetween reads the commits after base up to head from the commit
// list, which compares two revisions.
func (p *Provider) CommitsBetween(ctx context.Context, a provider.Account, r *provider.Repo, base, head string) ([]provider.Commit, error) {
	if head == "" {
		p.log.DebugContext(ctx, "deployment names no commit; commits between revisions unknown", "repo", r.FullName)
		return nil, nil
	}
	f := p.client()
	commits := p.organization(a.Login) + "/_apis/git/repositories/" + repoID(r.ID) + "/commits"
	carried := func(c adoCommit) (provider.Commit, bool) {
		at := ""
		if c.Author != nil {
			at = c.Author.Date
		}
		if at == "" && c.Committer != nil {
			at = c.Committer.Date
		}
		t, err := time.Parse(time.RFC3339, at)
		title, _, _ := strings.Cut(c.Comment, "\n")
		return provider.Commit{SHA: c.CommitID, AuthoredAt: t, Title: title}, err == nil
	}
	var found []adoCommit
	if base == "" {
		c, err := once[adoCommit](ctx, f, commits+"/"+url.PathEscape(head), nil)
		if provider.IsStatus(err, 404) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		found = []adoCommit{c}
	} else {
		compared, err := once[struct {
			Value []adoCommit `json:"value"`
		}](ctx, f, commits, q(
			"searchCriteria.itemVersion.version", base, "searchCriteria.itemVersion.versionType", "commit",
			"searchCriteria.compareVersion.version", head, "searchCriteria.compareVersion.versionType", "commit",
			"searchCriteria.$top", strconv.Itoa(compareLimit)))
		if provider.IsStatus(err, 404) {
			p.log.DebugContext(ctx, "commits between revisions unavailable", "repo", r.FullName, "base", base, "head", head)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if len(compared.Value) == compareLimit {
			p.log.WarnContext(ctx, "compare cut short; lead time counts the newest commits only", "repo", r.FullName, "base", base, "head", head, "listed", len(compared.Value))
		}
		found = compared.Value
	}
	// The forge lists newest first.
	out := []provider.Commit{}
	for i := len(found) - 1; i >= 0; i-- {
		if one, ok := carried(found[i]); ok {
			out = append(out, one)
		}
	}
	return out, nil
}
