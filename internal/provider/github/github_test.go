package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
)

const repoPath = "/repos/acme/mirror"

func routes() map[string]providertest.Route {
	r := func(file string) providertest.Route { return providertest.Route{File: file} }
	return map[string]providertest.Route{
		"GET " + repoPath:                                               {File: "repo.json", ETag: `"repo-1"`},
		"GET " + repoPath + "/actions/permissions":                      r("actions-permissions.json"),
		"GET " + repoPath + "/tags":                                     r("tags.json"),
		"GET " + repoPath + "/releases":                                 r("releases.json"),
		"GET " + repoPath + "/pulls":                                    r("pulls.json"),
		"GET " + repoPath + "/commits/p7/check-runs":                    r("check-runs-p7.json"),
		"GET " + repoPath + "/commits/p8/check-runs":                    r("check-runs-p8.json"),
		"GET " + repoPath + "/commits/p7/status":                        r("status-p7.json"),
		"GET " + repoPath + "/commits/p8/status":                        r("status-p8.json"),
		"GET " + repoPath + "/dependabot/alerts":                        r("dependabot.json"),
		"GET " + repoPath + "/code-scanning/alerts":                     r("code-scanning.json"),
		"GET " + repoPath + "/branches/main":                            r("branch.json"),
		"GET " + repoPath + "/rules/branches/main":                      r("rules.json"),
		"GET " + repoPath + "/contents/":                                r("contents-root.json"),
		"GET " + repoPath + "/contents/.github":                         r("contents-github.json"),
		"GET " + repoPath + "/deployments":                              r("deployments.json"),
		"GET " + repoPath + "/deployments/5/statuses":                   r("deployment-statuses.json"),
		"GET " + repoPath + "/issues":                                   r("issues.json"),
		"GET " + repoPath + "/actions/workflows":                        r("workflows.json"),
		"GET " + repoPath + "/actions/runs":                             r("runs.json"),
		"GET " + repoPath + "/contents/.github/workflows/pipeline.yaml": r("workflow-file.json"),
		"GET " + repoPath + "/compare/c0...c2":                          r("compare.json"),
		"GET " + repoPath + "/actions/workflows/7/runs":                 {Body: `{"total_count":0,"workflow_runs":[]}`},
	}
}

func newProvider(t *testing.T, forge *providertest.Forge, cfg Config) *Provider {
	t.Helper()
	cfg.API = forge.URL
	if cfg.Token == "" && cfg.AppID == "" {
		cfg.Token, cfg.TokenAccounts = "t", []string{"acme"}
	}
	p, err := New(cfg, providertest.Store(t), forge.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var acme = provider.Account{ID: "github:10", Provider: provider.GitHub, Login: "acme", Org: true}

func read(t *testing.T, p *Provider) *provider.Repo {
	t.Helper()
	r, err := p.Sync(context.Background(), acme, provider.RepoRef{ID: "github:1", FullName: "acme/mirror"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestSyncNormalisesARepository(t *testing.T) {
	r := read(t, newProvider(t, providertest.New(t, routes()), Config{}))
	if r.ID != "github:1" || r.FullName != "acme/mirror" || r.Visibility != "private" || r.Language != "Go" || r.DefaultBranch != "main" {
		t.Fatalf("repo = %+v", r)
	}
	if r.OpenPRs != 2 || r.OpenIssues != 1 {
		t.Errorf("open prs %d issues %d, want 2 and 1", r.OpenPRs, r.OpenIssues)
	}
	if len(r.Releases) != 1 || r.Releases[0].Tag != "v1.2.0" || r.Releases[0].Name != "v1.2.0" {
		t.Errorf("releases = %+v; drafts are left out and a release without a name takes its tag", r.Releases)
	}
	if *r.Dependabot != (provider.Alerts{Critical: 2, High: 1, URL: "https://github.com/acme/mirror/security/dependabot"}) {
		t.Errorf("dependabot = %+v", r.Dependabot)
	}
	if r.CodeScanning.Critical != 1 || r.CodeScanning.High != 1 {
		t.Errorf("code scanning = %+v; an error rule with no security level counts as high", r.CodeScanning)
	}
	if got := []string{r.PullRequests[0].Checks, r.PullRequests[1].Checks}; !reflect.DeepEqual(got, []string{provider.ChecksFailure, provider.ChecksNone}) {
		t.Errorf("checks = %v", got)
	}
	if len(r.Issues) != 1 || r.Issues[0].Number != 3 || r.Issues[0].Milestone != "v2" || r.Issues[0].Type != "Bug" {
		t.Errorf("issues = %+v; pull requests are left out of the issue list", r.Issues)
	}
	f := r.Facts
	if !*f.BranchProtected || !*f.RequiresReview || *f.RequiredApprovals != 1 || !*f.HasReadme || !*f.HasDockerfile || !*f.HasCodeowners ||
		f.LastCommitter != "a-dev" || f.LastCommitterEmail != "dev@example.com" {
		t.Errorf("facts = %+v", f)
	}
}

func TestSyncReadsPipelinesAndTheirRuns(t *testing.T) {
	r := read(t, newProvider(t, providertest.New(t, routes()), Config{}))
	if len(r.Pipelines) != 1 {
		t.Fatalf("pipelines = %d; a deleted workflow is left out", len(r.Pipelines))
	}
	p := r.Pipelines[0]
	if p.State != provider.Enabled || !reflect.DeepEqual(p.Schedules, []provider.Schedule{{Cron: "0 6 * * *", TimeZone: "UTC"}}) {
		t.Errorf("pipeline = %+v", p)
	}
	var triggers []string
	for _, run := range p.Runs {
		triggers = append(triggers, run.Trigger+":"+run.Conclusion)
	}
	if want := []string{"push:failure", "manual:success", "tag:failure", "push:success"}; !reflect.DeepEqual(triggers, want) {
		t.Errorf("runs = %v, want %v newest first, a push of a tag read as a tag", triggers, want)
	}
}

func TestSyncReadsHowEachDeploymentEnded(t *testing.T) {
	r := read(t, newProvider(t, providertest.New(t, routes()), Config{}))
	finished := at("2026-09-30T01:02:00Z")
	want := []provider.Deployment{{ID: "5", Environment: "production", Status: provider.DeploySucceeded, Ref: "main", SHA: "abc", CreatedAt: at("2026-09-30T01:00:00Z"), FinishedAt: &finished, URL: "https://github.com/acme/mirror/actions/runs/1", Actor: "dev"}}
	if !reflect.DeepEqual(r.Deployments, want) {
		t.Errorf("deployments = %+v", r.Deployments)
	}
	none := routes()
	delete(none, "GET "+repoPath+"/deployments")
	if r := read(t, newProvider(t, providertest.New(t, none), Config{})); r.Deployments != nil {
		t.Errorf("a repo the forge keeps no deployments for has none, not an empty list: %+v", r.Deployments)
	}
}

func TestSyncMarksPipelinesDisabledWhenActionsIsOff(t *testing.T) {
	off := routes()
	off["GET "+repoPath+"/actions/permissions"] = providertest.Route{Body: `{"enabled":false}`}
	if s := read(t, newProvider(t, providertest.New(t, off), Config{})).Pipelines[0].State; s != provider.Disabled {
		t.Errorf("state = %s, want disabled", s)
	}
	unreadable := routes()
	unreadable["GET "+repoPath+"/actions/permissions"] = providertest.Route{Status: 403, Body: `{}`}
	if s := read(t, newProvider(t, providertest.New(t, unreadable), Config{})).Pipelines[0].State; s != provider.Enabled {
		t.Errorf("state = %s; an unreadable setting trusts the workflow's own state", s)
	}
}

func TestConditionalRequestsKeepFactsNotBodies(t *testing.T) {
	forge := providertest.New(t, routes())
	p := newProvider(t, forge, Config{})
	first := read(t, p)
	second := read(t, p)
	if forge.NotModified == 0 {
		t.Fatal("the second read sent no ETag back")
	}
	if first.Name != second.Name || first.Stars != second.Stars || !reflect.DeepEqual(first.PushedAt, second.PushedAt) {
		t.Errorf("a 304 gave different facts: %+v vs %+v", first, second)
	}
	kept, err := p.store.Response(context.Background(), "token "+forge.URL+repoPath)
	if err != nil || kept == nil {
		t.Fatalf("no kept answer: %v", err)
	}
	if strings.Contains(string(kept.Facts), "node_id") || strings.Contains(string(kept.Facts), "permissions") {
		t.Errorf("kept facts carry fields redgreen does not read: %s", kept.Facts)
	}
}

func TestCommitsBetween(t *testing.T) {
	p := newProvider(t, providertest.New(t, routes()), Config{})
	commits, err := p.CommitsBetween(context.Background(), acme, &provider.Repo{FullName: "acme/mirror"}, "c0", "c2")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Commit{{SHA: "c1", AuthoredAt: at("2026-09-29T00:00:00Z"), Title: "feat: add"}, {SHA: "c2", AuthoredAt: at("2026-09-29T12:00:00Z"), Title: "fix: it"}}
	if !reflect.DeepEqual(commits, want) {
		t.Errorf("commits = %+v", commits)
	}
	unknown, err := p.CommitsBetween(context.Background(), acme, &provider.Repo{FullName: "acme/mirror"}, "gone", "c2")
	if err != nil || unknown != nil {
		t.Errorf("a compare the forge cannot answer is unknown, not an error: %v %v", unknown, err)
	}
}

func TestWebhookChecksTheSignature(t *testing.T) {
	key, _ := ParseKey(testKey)
	p := &Provider{cfg: Config{WebhookSecret: "s3cret"}, app: &appAuth{key: key}}
	body := []byte(`{"repository":{"full_name":"acme/mirror"}}`)
	sign := func(secret string) http.Header {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(body)
		return http.Header{"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(m.Sum(nil))}, "X-Github-Event": {"push"}, "X-Github-Delivery": {"d1"}}
	}
	d, err := p.Webhook(context.Background(), sign("s3cret"), body)
	if err != nil || d.ID != "d1" || !reflect.DeepEqual(d.Repos, []string{"acme/mirror"}) || d.Rediscover {
		t.Fatalf("delivery = %+v, %v", d, err)
	}
	if _, err := p.Webhook(context.Background(), sign("wrong"), body); err == nil {
		t.Fatal("a delivery signed with another secret was taken")
	}
	h := sign("s3cret")
	h.Set("X-Github-Event", "installation_repositories")
	if d, _ := p.Webhook(context.Background(), h, body); !d.Rediscover {
		t.Fatal("an installation change must rediscover")
	}
}

func TestParseSchedules(t *testing.T) {
	got := ParseSchedules("name: sync\non:\n  push:\n    branches: [main]\n  schedule:\n    - cron: '0 6 * * *'\n    - cron: \"30 18 * * 1-5\"\njobs: {}\n")
	if want := []provider.Schedule{{Cron: "0 6 * * *", TimeZone: "UTC"}, {Cron: "30 18 * * 1-5", TimeZone: "UTC"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("schedules = %+v", got)
	}
	for _, y := range []string{"on: [push, pull_request]\njobs: {}", "on: push", "on: {schedule: [{cron: 1}]}", ":: not yaml ["} {
		if got := ParseSchedules(y); len(got) != 0 {
			t.Errorf("%q gave %+v", y, got)
		}
	}
}

func TestAppSignsAsItself(t *testing.T) {
	key, err := ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := (&appAuth{id: "42", key: key}).jwt(time.Unix(1_800_000_000, 0))
	if err != nil || strings.Count(jwt, ".") != 2 {
		t.Fatalf("jwt = %q, %v", jwt, err)
	}
}

func TestFilesKeepNothingForOneCommit(t *testing.T) {
	r := routes()
	r["GET "+repoPath+"/git/trees/head1"] = providertest.Route{ETag: `"t"`, Body: `{"truncated":false,"tree":[{"path":"main.go","type":"blob","sha":"b1"},{"path":"cmd","type":"tree","sha":"x"}]}`}
	r["GET "+repoPath+"/git/blobs/b1"] = providertest.Route{ETag: `"b1"`, Body: `{"encoding":"base64","content":"cGFja2FnZSBtYWlu\n"}`}
	forge := providertest.New(t, r)
	p := newProvider(t, forge, Config{})
	files, err := p.Files(context.Background(), acme, &provider.Repo{FullName: "acme/mirror", DefaultBranch: "main"})
	if err != nil || files.Commit() != "head1" {
		t.Fatalf("files %v %v", files, err)
	}
	paths, _ := files.List(context.Background())
	data, _ := files.Read(context.Background(), "main.go")
	if !reflect.DeepEqual(paths, []string{"main.go"}) || string(data) != "package main" {
		t.Fatalf("paths %v data %q", paths, data)
	}
	ctx := context.Background()
	if kept, _ := p.store.Response(ctx, "token "+forge.URL+repoPath+"/git/trees/head1?recursive=1"); kept != nil {
		t.Error("a tree at one commit is never asked for again; keeping it only grows the state")
	}
	if kept, _ := p.store.Response(ctx, "token "+forge.URL+repoPath+"/git/blobs/b1"); kept == nil {
		t.Error("a blob is addressed by its content and asked for again on the next commit; it is kept")
	}
}

func TestWebhookReportsAJobWithoutReadingItsRepoAgain(t *testing.T) {
	key, _ := ParseKey(testKey)
	p := &Provider{cfg: Config{WebhookSecret: "s3cret"}, app: &appAuth{key: key}}
	deliver := func(job string) provider.Delivery {
		body := []byte(`{"repository":{"full_name":"acme/app"},"workflow_job":` + job + `}`)
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write(body)
		d, err := p.Webhook(context.Background(), http.Header{"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(m.Sum(nil))}, "X-Github-Event": {"workflow_job"}}, body)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Repos) != 0 {
			t.Fatalf("a job delivery asked to read %v again", d.Repos)
		}
		return d
	}
	cases := []struct {
		name, job, runner, os, status string
		started                       bool
	}{
		{"queued for a hosted image", `{"id":7,"status":"queued","created_at":"2026-11-08T12:00:00Z","started_at":"2026-11-08T12:00:00Z","labels":["ubuntu-latest"]}`, provider.RunnerHosted, "linux", provider.JobQueued, false},
		{"queued for a self-hosted label", `{"id":7,"status":"queued","created_at":"2026-11-08T12:00:00Z","labels":["house"]}`, provider.RunnerSelfHosted, "linux", provider.JobQueued, false},
		{"taken by a hosted runner", `{"id":7,"status":"in_progress","created_at":"2026-11-08T12:00:00Z","started_at":"2026-11-08T12:00:30Z","labels":["macos-15"],"runner_group_name":"GitHub Actions"}`, provider.RunnerHosted, "macos", provider.JobInProgress, true},
		{"taken by a self-hosted runner", `{"id":7,"status":"completed","conclusion":"success","created_at":"2026-11-08T12:00:00Z","started_at":"2026-11-08T12:00:30Z","completed_at":"2026-11-08T12:02:00Z","labels":["house"],"runner_group_name":"Default"}`, provider.RunnerSelfHosted, "linux", provider.JobCompleted, true},
	}
	for _, c := range cases {
		j := deliver(c.job).Job
		if j == nil || j.ID != "7" || j.Repo != "acme/app" || j.Runner != c.runner || j.OS != c.os || j.Status != c.status || j.StartedAt.IsZero() == c.started {
			t.Errorf("%s: job = %+v", c.name, j)
		}
	}
	if d := deliver(`{"id":7,"status":"waiting","labels":["ubuntu-latest"]}`); d.Job != nil {
		t.Errorf("a job waiting for approval was reported: %+v", d.Job)
	}
}
