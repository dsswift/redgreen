package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
	"github.com/dsswift/redgreen/internal/telemetry"
	"gopkg.in/yaml.v3"
)

// Config is how redgreen signs in to GitHub. An app wins over a token.
type Config struct {
	// API is GitHub's REST root; empty is api.github.com.
	API           string
	AppID         string
	PrivateKey    string
	WebhookSecret string
	// Allowed are the logins whose installations sync. Anyone else who installs the app is logged and left alone.
	Allowed []string
	// Token and TokenAccounts are a personal token for a local run: no webhooks, one shared rate limit.
	Token         string
	TokenAccounts []string
}

// Provider is GitHub.
type Provider struct {
	cfg    Config
	app    *appAuth
	store  *state.Store
	http   *http.Client
	quotas *provider.Quotas
	log    *slog.Logger
}

// New builds the provider. It fails only on a private key it cannot read.
func New(cfg Config, store *state.Store, client *http.Client, log *slog.Logger) (*Provider, error) {
	if cfg.API == "" {
		cfg.API = API
	}
	p := &Provider{cfg: cfg, store: store, http: client, quotas: &provider.Quotas{Provider: "github", Remaining: "X-RateLimit-Remaining", Limit: "X-RateLimit-Limit"}, log: log}
	switch {
	case cfg.AppID != "" && cfg.PrivateKey != "":
		key, err := ParseKey(cfg.PrivateKey)
		if err != nil {
			return nil, err
		}
		p.app = &appAuth{api: cfg.API, id: cfg.AppID, key: key, http: client}
		log.Info("github credentials: app", "appId", cfg.AppID, "allowed", cfg.Allowed, "webhooks", cfg.WebhookSecret != "")
	case cfg.Token != "":
		log.Info("github credentials: personal token", "accounts", cfg.TokenAccounts)
	default:
		log.Info("github off: neither GITHUB_APP_ID with GITHUB_APP_PRIVATE_KEY nor GITHUB_TOKEN is set")
	}
	return p, nil
}

func (p *Provider) Kind() provider.Kind       { return provider.GitHub }
func (p *Provider) Configured() bool          { return p.app != nil || p.cfg.Token != "" }
func (p *Provider) Quotas() []telemetry.Quota { return p.quotas.List() }
func (p *Provider) allowed(login string) bool {
	return slices.ContainsFunc(p.cfg.Allowed, func(a string) bool { return strings.EqualFold(a, login) })
}
func (p *Provider) fetcher(scope string, auth provider.Auth) *provider.Fetcher {
	return &provider.Fetcher{Store: p.store, HTTP: p.http, Scope: scope, Auth: auth, Quotas: p.quotas, Log: p.log}
}

// client acts as the account: its installation, or the token.
func (p *Provider) client(a provider.Account) *provider.Fetcher {
	if p.app != nil && a.Conn != "" {
		return p.fetcher("installation:"+a.Conn, p.app.asInstallation(a.Conn))
	}
	token := p.cfg.Token
	return p.fetcher("token", func(context.Context) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer " + token}, nil
	})
}

type ghInstallation struct {
	ID      int64   `json:"id"`
	Account ghOwner `json:"account"`
}

// Accounts lists the installations of the app whose account is allowed, or
// the accounts the token names.
func (p *Provider) Accounts(ctx context.Context) ([]provider.Account, error) {
	if p.app == nil {
		f := p.client(provider.Account{})
		var out []provider.Account
		for _, login := range p.cfg.TokenAccounts {
			u, err := one[ghOwner](ctx, f, p.cfg.API+"/users/"+url.PathEscape(login))
			if err != nil {
				return nil, err
			}
			out = append(out, account(u, ""))
		}
		return out, nil
	}
	installs, err := list[ghInstallation](ctx, p.fetcher("app", p.app.asApp), p.cfg.API+"/app/installations?per_page=100")
	if err != nil {
		return nil, err
	}
	var out []provider.Account
	for _, i := range installs {
		if !p.allowed(i.Account.Login) {
			p.log.InfoContext(ctx, "installation not synced: its account is not in GITHUB_ALLOWED_ACCOUNTS", "login", i.Account.Login, "installation", i.ID)
			continue
		}
		out = append(out, account(i.Account, itoa(i.ID)))
	}
	return out, nil
}

func account(o ghOwner, installation string) provider.Account {
	return provider.Account{ID: "github:" + itoa(o.ID), Provider: provider.GitHub, Login: o.Login, Name: o.Login, Org: o.Type == "Organization", URL: o.HTMLURL, Conn: installation}
}

// Repos lists what the installation may read, or what the token sees of the account.
func (p *Provider) Repos(ctx context.Context, a provider.Account) ([]provider.RepoRef, error) {
	f := p.client(a)
	var repos []ghRepo
	var err error
	switch {
	case a.Conn != "":
		repos, err = listOf(ctx, f, p.cfg.API+"/installation/repositories?per_page=100", func(b []byte) ([]ghRepo, error) {
			var page struct {
				Repositories []ghRepo `json:"repositories"`
			}
			err := json.Unmarshal(b, &page)
			return page.Repositories, err
		})
	case a.Org:
		repos, err = list[ghRepo](ctx, f, p.cfg.API+"/orgs/"+url.PathEscape(a.Login)+"/repos?type=all&per_page=100")
	default:
		var me ghOwner
		if me, err = one[ghOwner](ctx, f, p.cfg.API+"/user"); err != nil {
			return nil, err
		}
		if strings.EqualFold(me.Login, a.Login) {
			repos, err = list[ghRepo](ctx, f, p.cfg.API+"/user/repos?affiliation=owner&per_page=100")
		} else {
			repos, err = list[ghRepo](ctx, f, p.cfg.API+"/users/"+url.PathEscape(a.Login)+"/repos?per_page=100")
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]provider.RepoRef, 0, len(repos))
	for _, r := range repos {
		out = append(out, provider.RepoRef{ID: "github:" + itoa(r.ID), FullName: r.FullName})
	}
	return out, nil
}

// Sync reads one repository.
func (p *Provider) Sync(ctx context.Context, a provider.Account, r provider.RepoRef) (*provider.Repo, error) {
	repo, err := syncRepo(ctx, p.cfg.API, p.client(a), a, r.FullName, p.log)
	if err != nil {
		return nil, err
	}
	if repo.ID != r.ID {
		return nil, fmt.Errorf("repo id changed from %s to %s", r.ID, repo.ID)
	}
	return repo, nil
}

// compareLimit is the most commits a compare lists; a longer range is read in part and says so.
const compareLimit = 250

// CommitsBetween reads the commits after base up to head through the compare endpoint.
func (p *Provider) CommitsBetween(ctx context.Context, a provider.Account, r *provider.Repo, base, head string) ([]provider.Commit, error) {
	f := p.client(a)
	repo := p.cfg.API + "/repos/" + r.FullName
	carried := func(c ghCommit) (provider.Commit, bool) {
		at := ""
		if c.Commit.Author != nil {
			at = c.Commit.Author.Date
		}
		if at == "" && c.Commit.Committer != nil {
			at = c.Commit.Committer.Date
		}
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return provider.Commit{}, false
		}
		title, _, _ := strings.Cut(c.Commit.Message, "\n")
		return provider.Commit{SHA: c.SHA, AuthoredAt: t, Title: title}, true
	}
	if base == "" {
		c, err := one[ghCommit](ctx, f, repo+"/commits/"+url.PathEscape(head))
		if provider.IsStatus(err, 404, 422) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		out := []provider.Commit{}
		if one, ok := carried(c); ok {
			out = append(out, one)
		}
		return out, nil
	}
	cmp, err := one[struct {
		Total   int        `json:"total_commits"`
		Commits []ghCommit `json:"commits"`
	}](ctx, f, repo+"/compare/"+url.PathEscape(base+"..."+head)+fmt.Sprintf("?per_page=%d", compareLimit))
	if provider.IsStatus(err, 404, 422) {
		p.log.DebugContext(ctx, "commits between revisions unavailable", "repo", r.FullName, "base", base, "head", head)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if cmp.Total > len(cmp.Commits) {
		p.log.WarnContext(ctx, "compare truncated by github; lead time counts the oldest commits only", "repo", r.FullName, "base", base, "head", head, "total", cmp.Total, "listed", len(cmp.Commits))
	}
	out := []provider.Commit{}
	for _, c := range cmp.Commits {
		if one, ok := carried(c); ok {
			out = append(out, one)
		}
	}
	return out, nil
}

// Files reads the repository's tree at the head of its default branch. Files
// are read as blobs by hash, so a read is answered from kept facts for good.
func (p *Provider) Files(ctx context.Context, a provider.Account, r *provider.Repo) (services.Files, error) {
	f := p.client(a)
	repo := p.cfg.API + "/repos/" + r.FullName
	b, err := one[ghBranch](ctx, f, repo+"/branches/"+url.PathEscape(r.DefaultBranch))
	if provider.IsStatus(err, 404, 409) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &files{f: f, repo: repo, commit: b.Commit.SHA, log: p.log, name: r.FullName}, nil
}

type files struct {
	f      *provider.Fetcher
	repo   string
	commit string
	name   string
	blobs  map[string]string
	log    *slog.Logger
}

func (x *files) Commit() string { return x.commit }

func (x *files) List(ctx context.Context) ([]string, error) {
	tree, err := provider.Once(ctx, x.f, x.repo+"/git/trees/"+x.commit+"?recursive=1", provider.JSON[struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}])
	if err != nil {
		return nil, err
	}
	if tree.Truncated {
		x.log.WarnContext(ctx, "file tree truncated by github; services are read from part of it", "repo", x.name, "files", len(tree.Tree))
	}
	x.blobs = map[string]string{}
	var out []string
	for _, item := range tree.Tree {
		if item.Type == "blob" && item.Path != "" && item.SHA != "" {
			x.blobs[item.Path] = item.SHA
			out = append(out, item.Path)
		}
	}
	return out, nil
}

// Read reads a blob. Its kept facts are the file's bytes, so only files
// detection asks for are kept, each under its own hash.
func (x *files) Read(ctx context.Context, path string) ([]byte, error) {
	sha, ok := x.blobs[path]
	if !ok {
		return nil, nil
	}
	return provider.Get(ctx, x.f, x.repo+"/git/blobs/"+sha, func(a provider.Answer) ([]byte, error) {
		var blob struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(a.Body, &blob); err != nil {
			return nil, err
		}
		if blob.Encoding == "base64" {
			return base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
		}
		return []byte(blob.Content), nil
	})
}

func (x *files) Commits(ctx context.Context, path string) ([]services.Commit, error) {
	u := fmt.Sprintf("%s/commits?sha=%s&per_page=%d", x.repo, x.commit, services.HumanLookback)
	if path != "" {
		u += "&path=" + url.QueryEscape(path)
	}
	list, err := provider.Once(ctx, x.f, u, provider.JSON[[]ghCommit])
	if err != nil {
		return nil, err
	}
	out := make([]services.Commit, 0, len(list))
	for _, c := range list {
		var sc services.Commit
		if c.Commit.Committer != nil {
			sc.At = c.Commit.Committer.Date
		}
		if a := c.Commit.Author; a != nil {
			if sc.At == "" {
				sc.At = a.Date
			}
			sc.By, sc.Email = a.Name, a.Email
		}
		if c.Author != nil {
			if c.Author.Login != "" {
				sc.By = c.Author.Login
			}
			if sc.Email == "" && c.Author.Type == "Bot" {
				sc.Email = "[bot]"
			}
		}
		out = append(out, sc)
	}
	return out, nil
}

// Webhook verifies a delivery by its HMAC signature and says what to read again.
func (p *Provider) Webhook(_ context.Context, h http.Header, body []byte) (provider.Delivery, error) {
	secret := p.cfg.WebhookSecret
	if p.app == nil || secret == "" {
		return provider.Delivery{}, errors.New("webhooks need a GitHub App with GITHUB_APP_WEBHOOK_SECRET")
	}
	sig := strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
	want := hmac.New(sha256.New, []byte(secret))
	want.Write(body)
	given, err := hex.DecodeString(sig)
	if err != nil || !hmac.Equal(given, want.Sum(nil)) {
		return provider.Delivery{}, errors.New("signature does not match")
	}
	d := provider.Delivery{ID: h.Get("X-GitHub-Delivery"), Event: h.Get("X-GitHub-Event")}
	var payload struct {
		Repository *struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		WorkflowJob *ghJob `json:"workflow_job"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return provider.Delivery{}, fmt.Errorf("payload: %w", err)
	}
	if d.Event == "workflow_job" {
		// A job changes state three times a run; the workflow_run delivery
		// already reads the repository again, so a job only reports itself.
		if payload.WorkflowJob != nil && payload.Repository != nil {
			d.Job = payload.WorkflowJob.job(payload.Repository.FullName)
		}
		return d, nil
	}
	if payload.Repository != nil {
		d.Repos = []string{payload.Repository.FullName}
	}
	d.Rediscover = d.Event == "installation" || d.Event == "installation_repositories" || d.Event == "repository"
	return d, nil
}

// ghJob is the workflow_job of a webhook.
type ghJob struct {
	ID              int64      `json:"id"`
	Status          string     `json:"status"`
	Conclusion      string     `json:"conclusion"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	Labels          []string   `json:"labels"`
	RunnerGroupName string     `json:"runner_group_name"`
}

// hostedGroup is the runner group GitHub's own machines belong to.
const hostedGroup = "GitHub Actions"

// job is the neutral job, or nil for a state that says nothing about
// runners, such as waiting for an environment's approval.
func (j *ghJob) job(repo string) *provider.Job {
	switch j.Status {
	case provider.JobQueued, provider.JobInProgress, provider.JobCompleted:
	default:
		return nil
	}
	out := &provider.Job{ID: strconv.FormatInt(j.ID, 10), Repo: repo, Status: j.Status, Conclusion: j.Conclusion,
		Runner: j.runner(), OS: jobOS(j.Labels), CreatedAt: j.CreatedAt}
	if j.StartedAt != nil && j.Status != provider.JobQueued {
		out.StartedAt = *j.StartedAt
	}
	if j.CompletedAt != nil {
		out.CompletedAt = *j.CompletedAt
	}
	return out
}

// runner is the runner group that took the job, or before one has, what
// its labels ask for: a job asking only for GitHub's images is hosted.
func (j *ghJob) runner() string {
	if j.RunnerGroupName != "" {
		if j.RunnerGroupName == hostedGroup {
			return provider.RunnerHosted
		}
		return provider.RunnerSelfHosted
	}
	if len(j.Labels) == 0 {
		return provider.RunnerSelfHosted
	}
	for _, l := range j.Labels {
		if !hostedImage(l) {
			return provider.RunnerSelfHosted
		}
	}
	return provider.RunnerHosted
}

func hostedImage(label string) bool {
	l := strings.ToLower(label)
	for _, p := range []string{"ubuntu-", "windows-", "macos-"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

func jobOS(labels []string) string {
	for _, l := range labels {
		l = strings.ToLower(l)
		switch {
		case strings.HasPrefix(l, "macos"):
			return "macos"
		case strings.HasPrefix(l, "windows"):
			return "windows"
		}
	}
	return "linux"
}

// ParseSchedules reads the schedules a workflow file declares under
// on.schedule. GitHub evaluates them in UTC.
func ParseSchedules(workflow string) []provider.Schedule {
	var doc struct {
		On any `yaml:"on"`
	}
	out := []provider.Schedule{}
	if yaml.Unmarshal([]byte(workflow), &doc) != nil {
		return out
	}
	on, ok := doc.On.(map[string]any)
	if !ok {
		return out
	}
	entries, ok := on["schedule"].([]any)
	if !ok {
		return out
	}
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			if cron, ok := m["cron"].(string); ok {
				out = append(out, provider.Schedule{Cron: strings.TrimSpace(cron), TimeZone: "UTC"})
			}
		}
	}
	return out
}
