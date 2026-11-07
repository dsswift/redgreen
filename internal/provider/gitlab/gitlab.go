package gitlab

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// Config is how redgreen signs in to GitLab. An OAuth application wins over a token.
type Config struct {
	URL     string
	Allowed []string
	// ClientID, ClientSecret, and RefreshToken are an OAuth application and
	// the refresh token `redgreen setup gitlab` printed. GitLab replaces the
	// refresh token on every use, so the newest is kept in the state, sealed.
	ClientID, ClientSecret, RefreshToken string
	WebhookSecret                        string
	// Token and TokenAccounts are a personal or group access token.
	Token         string
	TokenAccounts []string
	// SecretKey seals the OAuth tokens kept in the state.
	SecretKey string
}

// tokenSetting is where the newest OAuth tokens are kept.
const tokenSetting = "gitlab.token"

// refreshAhead is how long before expiry an access token is replaced.
const refreshAhead = 5 * time.Minute

// Provider is GitLab.
type Provider struct {
	cfg    Config
	store  *state.Store
	http   *http.Client
	quotas *provider.Quotas
	log    *slog.Logger
	box    *provider.Box

	mu     sync.Mutex
	tokens *Tokens
	broken bool
}

// Tokens is what GitLab hands back. The refresh token is single-use.
type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// New builds the provider.
func New(cfg Config, store *state.Store, client *http.Client, log *slog.Logger) (*Provider, error) {
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	p := &Provider{cfg: cfg, store: store, http: client, quotas: &provider.Quotas{Provider: "gitlab", Remaining: "RateLimit-Remaining", Limit: "RateLimit-Limit"}, log: log}
	switch {
	case cfg.ClientID != "" && cfg.ClientSecret != "":
		box, err := provider.NewBox(cfg.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("gitlab oauth keeps its rotating tokens sealed: %w", err)
		}
		p.box = box
		log.Info("gitlab credentials: oauth application", "url", cfg.URL, "allowed", cfg.Allowed, "seeded", cfg.RefreshToken != "")
	case cfg.Token != "":
		log.Info("gitlab credentials: access token", "url", cfg.URL, "accounts", cfg.TokenAccounts)
	default:
		log.Info("gitlab off: neither GITLAB_CLIENT_ID with GITLAB_CLIENT_SECRET nor GITLAB_TOKEN is set")
	}
	return p, nil
}

func (p *Provider) Kind() provider.Kind       { return provider.GitLab }
func (p *Provider) Quotas() []telemetry.Quota { return p.quotas.List() }

// Configured is true with an access token, or with an OAuth application that holds a usable refresh token.
func (p *Provider) Configured() bool {
	if p.cfg.Token != "" && p.box == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.box != nil && !p.broken
}

func (p *Provider) oauth() bool { return p.box != nil }

func (p *Provider) client() *provider.Fetcher {
	auth := func(ctx context.Context) (map[string]string, error) {
		if !p.oauth() {
			return map[string]string{"PRIVATE-TOKEN": p.cfg.Token}, nil
		}
		t, err := p.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]string{"Authorization": "Bearer " + t}, nil
	}
	scope := "token"
	if p.oauth() {
		scope = "oauth"
	}
	return &provider.Fetcher{Store: p.store, HTTP: p.http, Scope: scope, Auth: auth, Quotas: p.quotas, Log: p.log}
}

// accessToken is the current access token, refreshed shortly before it
// expires. A refresh token GitLab no longer takes cannot be retried; the
// owner runs `redgreen setup gitlab` again.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken {
		return "", errors.New("gitlab authorization dropped; run `redgreen setup gitlab` and set GITLAB_REFRESH_TOKEN")
	}
	if p.tokens == nil {
		var sealed Tokens
		ok, err := p.store.Setting(ctx, tokenSetting, &sealed)
		if err != nil {
			return "", err
		}
		if ok {
			if p.tokens, err = p.open(sealed); err != nil {
				p.log.WarnContext(ctx, "kept gitlab tokens unreadable; starting from GITLAB_REFRESH_TOKEN", "error", err)
				p.tokens = nil
			}
		}
		if p.tokens == nil {
			if p.cfg.RefreshToken == "" {
				p.broken = true
				return "", errors.New("gitlab oauth needs GITLAB_REFRESH_TOKEN from `redgreen setup gitlab`")
			}
			p.tokens = &Tokens{RefreshToken: p.cfg.RefreshToken}
		}
	}
	if time.Until(p.tokens.ExpiresAt) > refreshAhead {
		return p.tokens.AccessToken, nil
	}
	fresh, err := Exchange(ctx, p.http, p.cfg.URL, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {p.tokens.RefreshToken}, "client_id": {p.cfg.ClientID}, "client_secret": {p.cfg.ClientSecret}})
	if err != nil {
		p.broken = true
		p.log.ErrorContext(ctx, "gitlab token refresh failed; authorization dropped", "error", err)
		return "", err
	}
	p.tokens = fresh
	sealed, err := p.seal(*fresh)
	if err == nil {
		err = p.store.PutSetting(ctx, tokenSetting, sealed)
	}
	if err != nil {
		p.log.ErrorContext(ctx, "gitlab tokens not kept; a restart needs a new GITLAB_REFRESH_TOKEN", "error", err)
	}
	p.log.InfoContext(ctx, "gitlab token refreshed", "expiresAt", fresh.ExpiresAt)
	return fresh.AccessToken, nil
}

func (p *Provider) seal(t Tokens) (Tokens, error) {
	a, err := p.box.Seal(t.AccessToken)
	if err != nil {
		return Tokens{}, err
	}
	r, err := p.box.Seal(t.RefreshToken)
	return Tokens{AccessToken: a, RefreshToken: r, ExpiresAt: t.ExpiresAt}, err
}

func (p *Provider) open(t Tokens) (*Tokens, error) {
	a, err := p.box.Open(t.AccessToken)
	if err != nil {
		return nil, err
	}
	r, err := p.box.Open(t.RefreshToken)
	return &Tokens{AccessToken: a, RefreshToken: r, ExpiresAt: t.ExpiresAt}, err
}

// Exchange asks GitLab's token endpoint for tokens: a code or a refresh token.
func Exchange(ctx context.Context, client *http.Client, base string, form url.Values) (*Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "redgreen")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, &provider.HTTPError{Status: res.StatusCode, URL: req.URL.String(), Body: string(body)}
	}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		CreatedAt    int64  `json:"created_at"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	issued := time.Now()
	if t.CreatedAt > 0 {
		issued = time.Unix(t.CreatedAt, 0)
	}
	return &Tokens{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: issued.Add(time.Duration(t.ExpiresIn) * time.Second)}, nil
}

func (p *Provider) api(path string, q url.Values) string {
	s := p.cfg.URL + "/api/v4/" + strings.TrimPrefix(path, "/")
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

var nextLink = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

type page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

func list[T any](ctx context.Context, f *provider.Fetcher, u string) ([]T, error) {
	return pages[T](ctx, f, u, provider.Get[page[T]])
}

// pages reads every page through get: kept, or once.
func pages[T any](ctx context.Context, f *provider.Fetcher, u string, get func(context.Context, *provider.Fetcher, string, func(provider.Answer) (page[T], error)) (page[T], error)) ([]T, error) {
	var out []T
	for u != "" {
		pg, err := get(ctx, f, u, func(a provider.Answer) (page[T], error) {
			var items []T
			err := json.Unmarshal(a.Body, &items)
			nxt := ""
			if m := nextLink.FindStringSubmatch(a.Header.Get("Link")); m != nil {
				nxt = m[1]
			}
			return page[T]{Items: items, Next: nxt}, err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, pg.Items...)
		u = pg.Next
	}
	return out, nil
}

func one[T any](ctx context.Context, f *provider.Fetcher, u string) (T, error) {
	return provider.Get(ctx, f, u, provider.JSON[T])
}

func (p *Provider) allowed(path string) bool {
	return slices.ContainsFunc(p.cfg.Allowed, func(a string) bool { return strings.EqualFold(a, path) })
}

// Accounts lists the person and the top-level groups the application sees
// that are allowed, or the namespaces the token names.
func (p *Provider) Accounts(ctx context.Context) ([]provider.Account, error) {
	f := p.client()
	if !p.oauth() {
		var out []provider.Account
		for _, path := range p.cfg.TokenAccounts {
			a, err := p.lookup(ctx, f, path)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
		return out, nil
	}
	me, err := one[glUser](ctx, f, p.api("user", nil))
	if err != nil {
		return nil, err
	}
	groups, err := list[glGroup](ctx, f, p.api("groups", q("min_access_level", "10", "per_page", "100")))
	if err != nil {
		return nil, err
	}
	var out []provider.Account
	if p.allowed(me.Username) {
		out = append(out, userAccount(me))
	} else {
		p.log.InfoContext(ctx, "namespace not synced: not in GITLAB_ALLOWED_ACCOUNTS", "path", me.Username)
	}
	for _, g := range topLevel(groups) {
		if p.allowed(g.FullPath) {
			out = append(out, groupAccount(g))
		} else {
			p.log.InfoContext(ctx, "namespace not synced: not in GITLAB_ALLOWED_ACCOUNTS", "path", g.FullPath)
		}
	}
	return out, nil
}

// lookup finds a group or a user by path, for a token that names its namespaces.
func (p *Provider) lookup(ctx context.Context, f *provider.Fetcher, path string) (provider.Account, error) {
	g, err := one[glGroup](ctx, f, p.api("groups/"+url.PathEscape(path), q("with_projects", "false")))
	if err == nil {
		return groupAccount(g), nil
	}
	if !provider.IsStatus(err, 404) {
		return provider.Account{}, err
	}
	users, err := one[[]glUser](ctx, f, p.api("users", q("username", path)))
	if err != nil {
		return provider.Account{}, err
	}
	if len(users) == 0 {
		return provider.Account{}, fmt.Errorf("no gitlab group or user at %s", path)
	}
	return userAccount(users[0]), nil
}

func groupAccount(g glGroup) provider.Account {
	return provider.Account{ID: "gitlab:group:" + strconv.FormatInt(g.ID, 10), Provider: provider.GitLab, Login: g.FullPath, Name: g.Name, Org: true, URL: g.WebURL}
}

func userAccount(u glUser) provider.Account {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = u.Username
	}
	return provider.Account{ID: "gitlab:user:" + strconv.FormatInt(u.ID, 10), Provider: provider.GitLab, Login: u.Username, Name: name, URL: u.WebURL}
}

func numericID(id string) (int64, error) {
	return strconv.ParseInt(id[strings.LastIndex(id, ":")+1:], 10, 64)
}

// Repos lists the account's projects, subgroups included.
func (p *Provider) Repos(ctx context.Context, a provider.Account) ([]provider.RepoRef, error) {
	id, err := numericID(a.ID)
	if err != nil {
		return nil, err
	}
	u := p.api(fmt.Sprintf("users/%d/projects", id), q("simple", "true", "per_page", "100"))
	if a.Org {
		u = p.api(fmt.Sprintf("groups/%d/projects", id), q("include_subgroups", "true", "with_shared", "false", "simple", "true", "per_page", "100"))
	}
	projects, err := list[glProject](ctx, p.client(), u)
	if err != nil {
		return nil, err
	}
	out := make([]provider.RepoRef, 0, len(projects))
	for _, pr := range projects {
		out = append(out, provider.RepoRef{ID: "gitlab:" + strconv.FormatInt(pr.ID, 10), FullName: pr.PathWithNamespace})
	}
	return out, nil
}

// Sync reads one project by its numeric id, so a rename between sweeps still finds it.
func (p *Provider) Sync(ctx context.Context, a provider.Account, r provider.RepoRef) (*provider.Repo, error) {
	id, err := numericID(r.ID)
	if err != nil {
		return nil, err
	}
	return p.sync(ctx, a, id)
}

// CommitsBetween reads the commits after base up to head through the compare endpoint.
func (p *Provider) CommitsBetween(ctx context.Context, _ provider.Account, r *provider.Repo, base, head string) ([]provider.Commit, error) {
	id, err := numericID(r.ID)
	if err != nil {
		return nil, err
	}
	type glCommit struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		AuthoredDate  string `json:"authored_date"`
		CommittedDate string `json:"committed_date"`
	}
	carried := func(c glCommit) (provider.Commit, bool) {
		at := c.AuthoredDate
		if at == "" {
			at = c.CommittedDate
		}
		t, err := time.Parse(time.RFC3339, at)
		return provider.Commit{SHA: c.ID, AuthoredAt: t, Title: c.Title}, err == nil
	}
	f := p.client()
	var commits []glCommit
	if base == "" {
		c, err := one[glCommit](ctx, f, p.api(fmt.Sprintf("projects/%d/repository/commits/%s", id, url.PathEscape(head)), nil))
		if provider.IsStatus(err, 404) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		commits = []glCommit{c}
	} else {
		cmp, err := one[struct {
			Commits []glCommit `json:"commits"`
		}](ctx, f, p.api(fmt.Sprintf("projects/%d/repository/compare", id), q("from", base, "to", head)))
		if provider.IsStatus(err, 404) {
			p.log.DebugContext(ctx, "commits between revisions unavailable", "project", id, "base", base, "head", head)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		commits = cmp.Commits
	}
	out := []provider.Commit{}
	for _, c := range commits {
		if one, ok := carried(c); ok {
			out = append(out, one)
		}
	}
	return out, nil
}

// Files reads a project's files at the head of its default branch. Read at the
// commit, so every answer stays kept until the branch moves.
func (p *Provider) Files(ctx context.Context, _ provider.Account, r *provider.Repo) (services.Files, error) {
	id, err := numericID(r.ID)
	if err != nil {
		return nil, err
	}
	f := p.client()
	b, err := one[glBranch](ctx, f, p.api(fmt.Sprintf("projects/%d/repository/branches/%s", id, url.PathEscape(r.DefaultBranch)), nil))
	if provider.IsStatus(err, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &files{p: p, f: f, id: id, commit: b.Commit.ID}, nil
}

type files struct {
	p      *Provider
	f      *provider.Fetcher
	id     int64
	commit string
}

func (x *files) Commit() string { return x.commit }

func (x *files) List(ctx context.Context) ([]string, error) {
	items, err := pages[struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}](ctx, x.f, x.p.api(fmt.Sprintf("projects/%d/repository/tree", x.id), q("ref", x.commit, "recursive", "true", "per_page", "100")), provider.Once)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, i := range items {
		if i.Type == "blob" {
			out = append(out, i.Path)
		}
	}
	return out, nil
}

func (x *files) Read(ctx context.Context, path string) ([]byte, error) {
	data, err := provider.Once(ctx, x.f, x.p.api(fmt.Sprintf("projects/%d/repository/files/%s", x.id, url.PathEscape(path)), q("ref", x.commit)), func(a provider.Answer) ([]byte, error) {
		var file struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(a.Body, &file); err != nil {
			return nil, err
		}
		if file.Encoding == "base64" {
			return base64.StdEncoding.DecodeString(file.Content)
		}
		return []byte(file.Content), nil
	})
	if provider.IsStatus(err, 404) {
		return nil, nil
	}
	return data, err
}

func (x *files) Commits(ctx context.Context, path string) ([]services.Commit, error) {
	query := q("ref_name", x.commit, "per_page", strconv.Itoa(services.HumanLookback))
	if path != "" {
		query.Set("path", path)
	}
	list, err := provider.Once(ctx, x.f, x.p.api(fmt.Sprintf("projects/%d/repository/commits", x.id), query), provider.JSON[[]struct {
		CommittedDate string `json:"committed_date"`
		AuthorName    string `json:"author_name"`
		AuthorEmail   string `json:"author_email"`
		CommitterName string `json:"committer_name"`
	}])
	if err != nil {
		return nil, err
	}
	out := make([]services.Commit, 0, len(list))
	for _, c := range list {
		by := c.AuthorName
		if by == "" {
			by = c.CommitterName
		}
		out = append(out, services.Commit{At: c.CommittedDate, By: by, Email: c.AuthorEmail})
	}
	return out, nil
}

// Webhook accepts a project or group webhook a person pointed at redgreen,
// checked by its secret token.
func (p *Provider) Webhook(_ context.Context, h http.Header, body []byte) (provider.Delivery, error) {
	secret := p.cfg.WebhookSecret
	if secret == "" {
		return provider.Delivery{}, errors.New("gitlab webhooks need GITLAB_WEBHOOK_SECRET")
	}
	given := h.Get("X-Gitlab-Token")
	if subtle.ConstantTimeCompare([]byte(given), []byte(secret)) != 1 {
		return provider.Delivery{}, errors.New("bad webhook token")
	}
	var payload struct {
		Project *struct {
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"project"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		return provider.Delivery{}, fmt.Errorf("payload: %w", err)
	}
	d := provider.Delivery{ID: h.Get("X-Gitlab-Event-UUID"), Event: h.Get("X-Gitlab-Event")}
	if payload.Project != nil && payload.Project.PathWithNamespace != "" {
		d.Repos = []string{payload.Project.PathWithNamespace}
	}
	return d, nil
}
