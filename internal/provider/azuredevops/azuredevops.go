package azuredevops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
	"github.com/dsswift/redgreen/internal/telemetry"
)

const (
	// Root is where every organisation lives.
	Root             = "https://dev.azure.com"
	advancedSecurity = "https://advsec.dev.azure.com"
	// apiVersion is asked for unless a call names its own; preview resources need theirs spelled out.
	apiVersion = "7.1"
	// resource is the application every organisation accepts tokens for.
	resource        = "499b84ac-1321-427f-aa17-267ca6975798"
	renewAhead      = 5 * time.Minute
	languagesTTL    = 15 * time.Minute
	runsPerPipeline = 30
	prChecksLimit   = 20
)

// Config is an Entra application added to each organisation as a user.
type Config struct {
	// Root, Security, and Login are the service's hosts; empty is the public cloud's.
	Root, Security, Login            string
	TenantID, ClientID, ClientSecret string
	Organizations                    []string
}

// Provider is Azure DevOps.
type Provider struct {
	cfg    Config
	store  *state.Store
	http   *http.Client
	quotas *provider.Quotas
	log    *slog.Logger

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	languages map[string]languageEntry
	// deployed holds each project's deployments, by organisation and project.
	deployed map[string]*projectDeployments
	// timelinesRead holds builds whose finished timeline was read, by when.
	timelinesRead map[string]time.Time
}

type languageEntry struct {
	at    time.Time
	found map[string]string
}

// New builds the provider.
func New(cfg Config, store *state.Store, client *http.Client, log *slog.Logger) *Provider {
	cfg.Organizations = organizationNames(cfg.Organizations)
	if cfg.Root == "" {
		cfg.Root = Root
	}
	if cfg.Security == "" {
		cfg.Security = advancedSecurity
	}
	if cfg.Login == "" {
		cfg.Login = "https://login.microsoftonline.com"
	}
	p := &Provider{cfg: cfg, store: store, http: client, quotas: &provider.Quotas{Provider: "azuredevops", Remaining: "X-RateLimit-Remaining", Limit: "X-RateLimit-Limit"}, log: log, languages: map[string]languageEntry{}, deployed: map[string]*projectDeployments{}, timelinesRead: map[string]time.Time{}}
	if p.Configured() {
		log.Info("azure devops credentials: service principal", "organizations", cfg.Organizations)
	} else {
		log.Info("azure devops off: AZURE_DEVOPS_TENANT_ID, AZURE_DEVOPS_CLIENT_ID, AZURE_DEVOPS_CLIENT_SECRET, and AZURE_DEVOPS_ORGANIZATIONS are not all set")
	}
	return p
}

func (p *Provider) Kind() provider.Kind       { return provider.AzureDevOps }
func (p *Provider) Quotas() []telemetry.Quota { return p.quotas.List() }
func (p *Provider) Configured() bool {
	return p.cfg.TenantID != "" && p.cfg.ClientID != "" && p.cfg.ClientSecret != "" && len(p.cfg.Organizations) > 0
}

var (
	orgURL    = regexp.MustCompile(`(?i)^https?://dev\.azure\.com/`)
	legacyURL = regexp.MustCompile(`(?i)^https?://([^./]+)\.visualstudio\.com.*$`)
	afterName = regexp.MustCompile(`/.*$`)
)

// OrganizationName is an organisation's name, from the name or from its URL.
func OrganizationName(input string) string {
	s := orgURL.ReplaceAllString(strings.TrimSpace(input), "")
	s = legacyURL.ReplaceAllString(s, "$1")
	return afterName.ReplaceAllString(s, "")
}

// organizationNames keeps each name once however it was cased.
func organizationNames(inputs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, in := range inputs {
		n := OrganizationName(in)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

func (p *Provider) organization(org string) string { return p.cfg.Root + "/" + url.PathEscape(org) }

// accessToken asks Entra for a token as the application itself. There is no
// refresh token; the next one is asked for the same way.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Until(p.expiresAt) > renewAhead {
		return p.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {p.cfg.ClientID}, "client_secret": {p.cfg.ClientSecret}, "scope": {resource + "/.default"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Login+"/"+url.PathEscape(p.cfg.TenantID)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "redgreen")
	res, err := p.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		p.log.ErrorContext(ctx, "azure devops token request failed", "status", res.StatusCode, "reason", entraReason(body))
		return "", fmt.Errorf("entra token request failed (%d): %s", res.StatusCode, entraReason(body))
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", err
	}
	p.token, p.expiresAt = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	p.log.InfoContext(ctx, "azure devops token issued", "expiresAt", p.expiresAt)
	return p.token, nil
}

// entraReason is the readable part of a refusal: what comes before its trace ids.
func entraReason(body []byte) string {
	var e struct {
		Description string `json:"error_description"`
		Error       string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return string(body[:min(len(body), 200)])
	}
	s := e.Description
	if s == "" {
		s = e.Error
	}
	s = regexp.MustCompile(`\r?\n|\s*Trace ID:`).Split(s, 2)[0]
	return s[:min(len(s), 300)]
}

func (p *Provider) client() *provider.Fetcher {
	return &provider.Fetcher{Store: p.store, HTTP: p.http, Scope: "service-principal", Quotas: p.quotas, Log: p.log, Redirects: true,
		Auth: func(ctx context.Context) (map[string]string, error) {
			t, err := p.accessToken(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]string{"Authorization": "Bearer " + t}, nil
		}}
}

// withQuery adds api-version unless the query names its own.
func withQuery(u string, query url.Values) string {
	parsed, _ := url.Parse(u)
	v := parsed.Query()
	v.Set("api-version", apiVersion)
	for k, vals := range query {
		v[k] = vals
	}
	parsed.RawQuery = v.Encode()
	return parsed.String()
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func get[T any](ctx context.Context, f *provider.Fetcher, u string, query url.Values) (T, error) {
	return provider.Get(ctx, f, withQuery(u, query), provider.JSON[T])
}

func once[T any](ctx context.Context, f *provider.Fetcher, u string, query url.Values) (T, error) {
	return provider.Once(ctx, f, withQuery(u, query), provider.JSON[T])
}

type page[T any] struct {
	Items        []T    `json:"items"`
	Continuation string `json:"continuation,omitempty"`
}

// list reads every item of a collection, following the continuation token.
func list[T any](ctx context.Context, f *provider.Fetcher, u string, query url.Values) ([]T, error) {
	return collect[T](ctx, f, u, query, provider.Get[page[T]], nil)
}

// collect reads pages through get, kept or once, to the last one, or until
// enough says the page just read is the last one wanted.
func collect[T any](ctx context.Context, f *provider.Fetcher, u string, query url.Values, get func(context.Context, *provider.Fetcher, string, func(provider.Answer) (page[T], error)) (page[T], error), enough func([]T) bool) ([]T, error) {
	var out []T
	cont := ""
	for {
		qq := url.Values{}
		for k, v := range query {
			qq[k] = v
		}
		if cont != "" {
			qq.Set("continuationToken", cont)
		}
		pg, err := get(ctx, f, withQuery(u, qq), func(a provider.Answer) (page[T], error) {
			var items []T
			if err := json.Unmarshal(a.Body, &items); err != nil {
				var wrapped struct {
					Value []T `json:"value"`
				}
				if err := json.Unmarshal(a.Body, &wrapped); err != nil {
					return page[T]{}, err
				}
				items = wrapped.Value
			}
			return page[T]{Items: items, Continuation: a.Header.Get("X-Ms-Continuationtoken")}, nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, pg.Items...)
		if pg.Continuation == "" || (enough != nil && enough(pg.Items)) {
			return out, nil
		}
		cont = pg.Continuation
	}
}

// Accounts are the organisations a person named. Naming one is the approval.
func (p *Provider) Accounts(context.Context) ([]provider.Account, error) {
	var out []provider.Account
	for _, org := range p.cfg.Organizations {
		out = append(out, provider.Account{ID: "azuredevops:org:" + strings.ToLower(org), Provider: provider.AzureDevOps, Login: org, Name: org, Org: true, URL: p.organization(org)})
	}
	return out, nil
}

// Repos lists every repository in the organisation.
func (p *Provider) Repos(ctx context.Context, a provider.Account) ([]provider.RepoRef, error) {
	repos, err := list[adoRepo](ctx, p.client(), p.organization(a.Login)+"/_apis/git/repositories", nil)
	if err != nil {
		return nil, err
	}
	out := make([]provider.RepoRef, 0, len(repos))
	for _, r := range repos {
		out = append(out, provider.RepoRef{ID: "azuredevops:" + r.ID, FullName: a.Login + "/" + r.Project.Name + "/" + r.Name})
	}
	return out, nil
}

// language is a repository's main language. One answer covers a whole
// project, so it is shared by the repositories a sweep reads.
func (p *Provider) language(ctx context.Context, org, project, repo string) (string, error) {
	key := org + "/" + project
	p.mu.Lock()
	e, ok := p.languages[key]
	p.mu.Unlock()
	if ok && time.Since(e.at) < languagesTTL {
		return e.found[repo], nil
	}
	m, err := get[adoLanguageMetrics](ctx, p.client(), p.organization(org)+"/"+project+"/_apis/projectanalysis/languagemetrics", q("api-version", "7.1-preview.1"))
	found := map[string]string{}
	switch {
	case err == nil:
		found = topLanguages(m)
	case provider.IsStatus(err, 403, 404):
		p.log.DebugContext(ctx, "language metrics unavailable", "org", org, "project", project)
	default:
		return "", err
	}
	p.mu.Lock()
	p.languages[key] = languageEntry{at: time.Now(), found: found}
	p.mu.Unlock()
	return found[repo], nil
}

type scope struct {
	org     string
	repo    adoRepo
	project string
	git     string
	branch  string
}

func repoID(id string) string { return id[strings.LastIndex(id, ":")+1:] }

// Sync reads one repository by its id, so a rename between sweeps still finds it.
func (p *Provider) Sync(ctx context.Context, a provider.Account, ref provider.RepoRef) (*provider.Repo, error) {
	f := p.client()
	org := a.Login
	repo, err := get[adoRepo](ctx, f, p.organization(org)+"/_apis/git/repositories/"+repoID(ref.ID), nil)
	if err != nil {
		return nil, err
	}
	s := scope{org: org, repo: repo, project: p.organization(org) + "/" + repo.Project.ID}
	s.git = s.project + "/_apis/git/repositories/" + repo.ID
	if repo.DefaultBranch != "" {
		s.branch = shortRef(repo.DefaultBranch)
	}
	branch := s.branch
	if branch == "" {
		branch = "main"
	}
	out := &provider.Repo{
		ID: "azuredevops:" + repo.ID, Provider: provider.AzureDevOps, AccountLogin: org, Name: repo.Name, FullName: org + "/" + repo.Project.Name + "/" + repo.Name,
		URL: repo.WebURL, DefaultBranch: branch, Visibility: visibility(repo), Archived: repo.IsDisabled, Fork: repo.IsFork, SyncedAt: time.Now().UTC(),
		Pipelines: []provider.Pipeline{}, PullRequests: []provider.PullRequest{},
	}
	if repo.IsDisabled {
		p.log.DebugContext(ctx, "disabled repository; skipping activity feeds", "repo", out.FullName)
		return out, nil
	}
	if out.Language, err = p.language(ctx, org, repo.Project.ID, repo.ID); err != nil {
		return nil, err
	}
	pushes, err := get[struct {
		Value []struct {
			Date time.Time `json:"date"`
		} `json:"value"`
	}](ctx, f, s.git+"/pushes", q("$top", "1"))
	if err != nil {
		return nil, err
	}
	if len(pushes.Value) > 0 {
		out.PushedAt = &pushes.Value[0].Date
	}
	pulls, err := get[struct {
		Value []adoPullRequest `json:"value"`
	}](ctx, f, s.git+"/pullrequests", q("searchCriteria.status", "active", "$top", "100"))
	if err != nil {
		return nil, err
	}
	if out.Dependabot, out.CodeScanning, err = p.security(ctx, f, s); err != nil {
		return nil, err
	}
	if out.Facts, err = p.facts(ctx, f, s); err != nil {
		return nil, err
	}
	if out.Pipelines, err = p.pipelines(ctx, f, s); err != nil {
		return nil, err
	}
	if out.Deployments, err = p.deployments(ctx, f, s); err != nil {
		return nil, err
	}
	out.OpenPRs = len(pulls.Value)
	for i, pr := range pulls.Value {
		item := provider.PullRequest{Number: pr.PullRequestID, Title: pr.Title, URL: fmt.Sprintf("%s/pullrequest/%d", repo.WebURL, pr.PullRequestID), Draft: pr.IsDraft, CreatedAt: pr.CreationDate, UpdatedAt: pr.CreationDate}
		if pr.CreatedBy != nil {
			item.Author = pr.CreatedBy.DisplayName
		}
		// A pull request is built on its merge ref; those builds are its checks.
		if i < prChecksLimit {
			builds, err := p.builds(ctx, f, s, q("branchName", fmt.Sprintf("refs/pull/%d/merge", pr.PullRequestID), "$top", "50"))
			switch {
			case err == nil:
				item.Checks = checksState(builds)
			case provider.IsStatus(err, 403, 404):
				item.Checks = provider.ChecksNone
			default:
				return nil, err
			}
		}
		out.PullRequests = append(out.PullRequests, item)
	}
	return out, nil
}

func (p *Provider) builds(ctx context.Context, f *provider.Fetcher, s scope, query url.Values) ([]adoBuild, error) {
	query.Set("repositoryId", s.repo.ID)
	query.Set("repositoryType", "TfsGit")
	query.Set("queryOrder", "queueTimeDescending")
	b, err := get[struct {
		Value []adoBuild `json:"value"`
	}](ctx, f, s.project+"/_apis/build/builds", query)
	return b.Value, err
}

// security reads alerts. They need Advanced Security switched on for the repository.
func (p *Provider) security(ctx context.Context, f *provider.Fetcher, s scope) (*provider.Alerts, *provider.Alerts, error) {
	u := fmt.Sprintf("%s/%s/%s/_apis/alert/repositories/%s/alerts", p.cfg.Security, url.PathEscape(s.org), s.repo.Project.ID, s.repo.ID)
	alerts, err := list[adoAlert](ctx, f, u, q("criteria.states", "active", "top", "500", "api-version", "7.2-preview.1"))
	var h *provider.HTTPError
	notEnabled := errors.As(err, &h) && h.Status == 400 && strings.Contains(h.Body, "Advanced Security is not enabled")
	if notEnabled || provider.IsStatus(err, 401, 403, 404) {
		p.log.DebugContext(ctx, "security alerts unavailable", "repo", s.repo.Name)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	dep, code := securityCounts(alerts, s.repo.WebURL)
	return dep, code, nil
}

func (p *Provider) facts(ctx context.Context, f *provider.Fetcher, s scope) (*provider.Facts, error) {
	if s.branch == "" {
		return facts(nil, false, nil, false, nil), nil
	}
	tolerate := func(err error) error {
		if provider.IsStatus(err, 403, 404) {
			return nil
		}
		return err
	}
	policies, err := list[adoPolicy](ctx, f, s.project+"/_apis/git/policy/configurations", q("repositoryId", s.repo.ID, "refName", "refs/heads/"+s.branch))
	policiesKnown := err == nil
	if err = tolerate(err); err != nil {
		return nil, err
	}
	items, err := list[struct {
		Path string `json:"path"`
	}](ctx, f, s.git+"/items", q("scopePath", "/", "recursionLevel", "OneLevel", "versionDescriptor.version", s.branch, "versionDescriptor.versionType", "branch"))
	rootKnown := err == nil
	if err = tolerate(err); err != nil {
		return nil, err
	}
	var root []string
	for _, i := range items {
		root = append(root, i.Path)
	}
	commits, err := get[struct {
		Value []adoCommit `json:"value"`
	}](ctx, f, s.git+"/commits", q("searchCriteria.itemVersion.version", s.branch, "searchCriteria.$top", "1"))
	if err = tolerate(err); err != nil {
		return nil, err
	}
	var last *adoCommit
	if len(commits.Value) > 0 {
		last = &commits.Value[0]
	}
	return facts(policies, policiesKnown, root, rootKnown, last), nil
}

func (p *Provider) pipelines(ctx context.Context, f *provider.Fetcher, s scope) ([]provider.Pipeline, error) {
	listed, err := list[adoDefinition](ctx, f, s.project+"/_apis/build/definitions", q("repositoryId", s.repo.ID, "repositoryType", "TfsGit", "includeAllProperties", "true"))
	if provider.IsStatus(err, 403, 404) {
		p.log.DebugContext(ctx, "pipelines unavailable", "repo", s.repo.Name)
		return []provider.Pipeline{}, nil
	}
	if err != nil {
		return nil, err
	}
	var definitions []adoDefinition
	for _, d := range listed {
		if d.Quality != "draft" {
			definitions = append(definitions, d)
		}
	}
	if len(definitions) == 0 {
		return []provider.Pipeline{}, nil
	}
	recent, err := p.builds(ctx, f, s, q("$top", "100"))
	if err != nil {
		return nil, err
	}
	branch := s.branch
	if branch == "" {
		branch = "main"
	}
	defaultRef := "refs/heads/" + branch
	out := []provider.Pipeline{}
	for _, d := range definitions {
		schedules, err := p.schedules(ctx, f, s, d)
		if err != nil {
			return nil, err
		}
		builds := map[int64]adoBuild{}
		for _, b := range recent {
			if b.Definition.ID == d.ID {
				builds[b.ID] = b
			}
		}
		has := func(ok func(adoBuild) bool) bool {
			for _, b := range builds {
				if ok(b) {
					return true
				}
			}
			return false
		}
		if !has(func(b adoBuild) bool {
			return b.SourceBranch == defaultRef && b.Reason != "pullRequest" && b.Status == "completed" && b.Result != "canceled" && b.Result != "none"
		}) {
			more, err := p.builds(ctx, f, s, q("definitions", strconv.FormatInt(d.ID, 10), "branchName", defaultRef, "$top", "10"))
			if err != nil {
				return nil, err
			}
			for _, b := range more {
				builds[b.ID] = b
			}
		}
		if len(schedules) > 0 && !has(func(b adoBuild) bool { return b.Reason == "schedule" }) {
			more, err := p.builds(ctx, f, s, q("definitions", strconv.FormatInt(d.ID, 10), "reasonFilter", "schedule", "$top", "1"))
			if err != nil {
				return nil, err
			}
			for _, b := range more {
				builds[b.ID] = b
			}
		}
		ordered := make([]adoBuild, 0, len(builds))
		for _, b := range builds {
			ordered = append(ordered, b)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].QueueTime.After(ordered[j].QueueTime) })
		if len(ordered) > runsPerPipeline {
			ordered = ordered[:runsPerPipeline]
		}
		runs := []provider.Run{}
		for _, b := range ordered {
			runs = append(runs, run(b))
		}
		out = append(out, pipeline(d, s.project, schedules, runs))
	}
	return out, nil
}

// schedules set in the pipeline editor replace the ones in the YAML file, so the file is read only without them.
func (p *Provider) schedules(ctx context.Context, f *provider.Fetcher, s scope, d adoDefinition) ([]provider.Schedule, error) {
	found := definitionSchedules(d)
	if found == nil {
		fs, err := p.fileSchedules(ctx, f, s, d)
		if err != nil {
			return nil, err
		}
		found = &fs
	}
	if found.Conditional > 0 {
		p.log.InfoContext(ctx, "schedule fires only after a change; not watched for missed runs", "repo", s.repo.Name, "pipeline", d.Name, "schedules", found.Conditional)
	}
	return found.Schedules, nil
}

func (p *Provider) fileSchedules(ctx context.Context, f *provider.Fetcher, s scope, d adoDefinition) (FileSchedules, error) {
	if d.Process == nil || d.Process.YAMLFilename == "" || s.branch == "" {
		return FileSchedules{}, nil
	}
	file, err := get[struct {
		Content string `json:"content"`
	}](ctx, f, s.git+"/items", q("path", "/"+strings.TrimPrefix(d.Process.YAMLFilename, "/"), "includeContent", "true", "$format", "json", "versionDescriptor.version", s.branch, "versionDescriptor.versionType", "branch"))
	if provider.IsStatus(err, 404) {
		return FileSchedules{}, nil
	}
	if err != nil {
		return FileSchedules{}, err
	}
	return ParseSchedules(file.Content), nil
}

// Files reads a repository's files at the head of its default branch. The whole tree comes in one call.
func (p *Provider) Files(ctx context.Context, a provider.Account, r *provider.Repo) (services.Files, error) {
	f := p.client()
	git := p.organization(a.Login) + "/_apis/git/repositories/" + repoID(r.ID)
	commits, err := get[struct {
		Value []adoCommit `json:"value"`
	}](ctx, f, git+"/commits", q("searchCriteria.itemVersion.version", r.DefaultBranch, "searchCriteria.$top", "1"))
	if provider.IsStatus(err, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(commits.Value) == 0 || commits.Value[0].CommitID == "" {
		return nil, nil
	}
	return &files{f: f, git: git, commit: commits.Value[0].CommitID}, nil
}

type files struct {
	f      *provider.Fetcher
	git    string
	commit string
}

func (x *files) Commit() string { return x.commit }

func (x *files) at() url.Values {
	return q("versionDescriptor.version", x.commit, "versionDescriptor.versionType", "commit")
}

func (x *files) List(ctx context.Context) ([]string, error) {
	query := x.at()
	query.Set("scopePath", "/")
	query.Set("recursionLevel", "Full")
	items, err := collect[struct {
		Path     string `json:"path"`
		IsFolder bool   `json:"isFolder"`
	}](ctx, x.f, x.git+"/items", query, provider.Once, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, i := range items {
		if !i.IsFolder {
			out = append(out, strings.TrimPrefix(i.Path, "/"))
		}
	}
	return out, nil
}

// Read reads one item; a binary file's content comes base64 encoded and says so.
func (x *files) Read(ctx context.Context, path string) ([]byte, error) {
	query := x.at()
	query.Set("path", "/"+path)
	query.Set("includeContent", "true")
	query.Set("$format", "json")
	data, err := provider.Once(ctx, x.f, withQuery(x.git+"/items", query), func(a provider.Answer) ([]byte, error) {
		var item struct {
			Content  *string `json:"content"`
			Metadata *struct {
				IsBinary bool `json:"isBinary"`
			} `json:"contentMetadata"`
		}
		if err := json.Unmarshal(a.Body, &item); err != nil {
			return nil, err
		}
		if item.Content == nil {
			return nil, nil
		}
		if item.Metadata != nil && item.Metadata.IsBinary {
			return base64.StdEncoding.DecodeString(*item.Content)
		}
		return []byte(*item.Content), nil
	})
	if provider.IsStatus(err, 404) {
		return nil, nil
	}
	return data, err
}

func (x *files) Commits(ctx context.Context, path string) ([]services.Commit, error) {
	found, err := once[struct {
		Value []adoCommit `json:"value"`
	}](ctx, x.f, x.git+"/commits", q("searchCriteria.itemVersion.version", x.commit, "searchCriteria.itemVersion.versionType", "commit", "searchCriteria.itemPath", "/"+path, "searchCriteria.$top", strconv.Itoa(services.HumanLookback)))
	if err != nil {
		return nil, err
	}
	out := make([]services.Commit, 0, len(found.Value))
	for _, c := range found.Value {
		var sc services.Commit
		if c.Committer != nil {
			sc.At = c.Committer.Date
		}
		if c.Author != nil {
			sc.By, sc.Email = c.Author.Name, c.Author.Email
		}
		out = append(out, sc)
	}
	return out, nil
}
