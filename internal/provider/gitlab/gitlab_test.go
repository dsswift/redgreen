package gitlab

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
)

func TestRunStatus(t *testing.T) {
	for in, want := range map[string][2]string{
		"success": {"completed", "success"}, "failed": {"completed", "failure"}, "canceled": {"completed", "cancelled"},
		"manual": {"completed", "action_required"}, "running": {"running", ""}, "pending": {"queued", ""}, "waiting_for_resource": {"queued", ""},
	} {
		s, c := runStatus(in)
		if [2]string{s, c} != want {
			t.Errorf("%s: %s %s, want %v", in, s, c, want)
		}
	}
}

func TestTriggers(t *testing.T) {
	tags := map[string]bool{"v1.0.0": true}
	for _, c := range []struct{ ref, source, want string }{
		{"v1.0.0", "push", "tag"}, {"main", "push", "push"}, {"main", "schedule", "schedule"}, {"main", "web", "manual"},
		{"feature", "merge_request_event", "pull_request"}, {"main", "api", "other"},
	} {
		if got := trigger(glPipeline{Ref: c.ref, Source: c.source}, tags); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.ref, c.source, got, c.want)
		}
	}
}

func TestChecksAndTimeZones(t *testing.T) {
	for in, want := range map[string]string{"success": "success", "failed": "failure", "running": "pending", "manual": "pending", "canceled": "none", "": "none"} {
		if got := checksState(in); got != want {
			t.Errorf("checks %q = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"America/New_York": "America/New_York", "Pacific Time (US & Canada)": "America/Los_Angeles", "UTC": "UTC", "Nowhere/Special": "UTC"} {
		if got := ianaTimeZone(in); got != want {
			t.Errorf("zone %q = %s, want %s", in, got, want)
		}
	}
}

func TestSecurityCountsNeedAScanner(t *testing.T) {
	url := "https://example.org/g/p"
	if d, c := securityCounts(nil, url); d != nil || c != nil {
		t.Fatal("no answer means no feeds")
	}
	s := &glSecurity{Dependency: &severityCount{Critical: 2, High: 1}, Code: &severityCount{High: 3}}
	s.Scanners = &struct {
		Enabled []string `json:"enabled"`
	}{Enabled: []string{"SAST"}}
	d, c := securityCounts(s, url)
	if d != nil || *c != (provider.Alerts{High: 3, URL: url + "/-/security/vulnerability_report"}) {
		t.Errorf("dependency %+v code %+v", d, c)
	}
}

func TestTopLevelGroups(t *testing.T) {
	var got []string
	for _, g := range topLevel([]glGroup{{FullPath: "acme"}, {FullPath: "acme/platform"}, {FullPath: "other"}, {FullPath: "acme-labs"}}) {
		got = append(got, g.FullPath)
	}
	if !reflect.DeepEqual(got, []string{"acme", "other", "acme-labs"}) {
		t.Errorf("groups = %v", got)
	}
}

const project = `{"id":42,"name":"api","path_with_namespace":"acme/api","web_url":"https://gitlab.example/acme/api","description":"The API","default_branch":"main",
"visibility":"internal","archived":false,"star_count":2,"open_issues_count":1,"last_activity_at":"2026-09-30T00:00:00Z","created_at":"2026-01-01T00:00:00Z",
"builds_access_level":"enabled","namespace":{"id":1,"full_path":"acme"},"topics":["svc"],"runners_token":"never-kept"}`

func gitlabRoutes() map[string]providertest.Route {
	b := func(s string) providertest.Route { return providertest.Route{Body: s} }
	return map[string]providertest.Route{
		"GET /api/v4/projects/42":                                 {Body: project, ETag: `W/"p1"`},
		"GET /api/v4/projects/42/languages":                       b(`{"Go": 80.5, "HCL": 19.5}`),
		"GET /api/v4/projects/42/repository/tags":                 b(`[{"name":"v1.0.0"}]`),
		"GET /api/v4/projects/42/releases":                        {Status: 403, Body: `{"message":"403 Forbidden"}`},
		"GET /api/v4/projects/42/merge_requests":                  b(`[{"iid":5,"title":"Speed up","web_url":"https://gitlab.example/acme/api/-/merge_requests/5","draft":false,"created_at":"2026-09-20T00:00:00Z","updated_at":"2026-09-21T00:00:00Z","labels":["perf"],"author":{"username":"dev"}}]`),
		"GET /api/v4/projects/42/merge_requests/5":                b(`{"head_pipeline":{"status":"failed"}}`),
		"GET /api/v4/projects/42/issues":                          b(`[{"iid":9,"title":"Broken link","web_url":"https://gitlab.example/acme/api/-/issues/9","labels":["docs"],"user_notes_count":1,"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","issue_type":"issue","author":{"username":"user1"},"assignees":[{"username":"dev"}],"milestone":null}]`),
		"POST /api/graphql":                                       b(`{"data":{"project":{"securityScanners":{"enabled":["DEPENDENCY_SCANNING"]},"dependency":{"critical":1,"high":0},"code":{"critical":0,"high":0}}}}`),
		"GET /api/v4/projects/42/deployments":                     b(`[{"id":77,"ref":"main","sha":"abc","status":"success","created_at":"2026-09-30T01:00:00Z","updated_at":"2026-09-30T01:05:00Z","environment":{"name":"production"},"user":{"username":"dev"},"deployable":{"finished_at":"2026-09-30T01:04:00Z","web_url":"https://gitlab.example/job/1"}}]`),
		"GET /api/v4/projects/42/repository/branches/main":        b(`{"name":"main","protected":true,"commit":{"id":"head","author_name":"A Developer","author_email":"Dev@Example.com","committed_date":"2026-09-30T00:00:00Z"}}`),
		"GET /api/v4/projects/42/protected_branches/main":         b(`{"name":"main"}`),
		"GET /api/v4/projects/42/approval_rules":                  {Status: 404, Body: `{"message":"404 Not found"}`},
		"GET /api/v4/projects/42/repository/tree":                 b(`[{"name":"README.md","type":"blob","path":"README.md"},{"name":"CODEOWNERS","type":"blob","path":"CODEOWNERS"}]`),
		"GET /api/v4/projects/42/pipelines":                       b(`[{"id":3,"iid":3,"name":null,"ref":"main","sha":"c3","status":"failed","source":"push","created_at":"2026-09-30T03:00:00Z","updated_at":"2026-09-30T03:10:00Z","web_url":"u3"},{"id":2,"iid":2,"name":null,"ref":"v1.0.0","sha":"c2","status":"success","source":"push","created_at":"2026-09-29T03:00:00Z","updated_at":"2026-09-29T03:10:00Z","web_url":"u2"},{"id":1,"iid":1,"name":null,"ref":"main","sha":"c1","status":"success","source":"schedule","created_at":"2026-09-28T03:00:00Z","updated_at":"2026-09-28T03:10:00Z","web_url":"u1"}]`),
		"GET /api/v4/projects/42/pipeline_schedules":              b(`[{"id":11,"description":"Nightly","cron":"0 2 * * *","cron_timezone":"Eastern Time (US & Canada)","active":true,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`),
		"GET /api/v4/projects/42/pipeline_schedules/11/pipelines": b(`[{"id":1,"iid":1,"name":null,"ref":"main","sha":"c1","status":"success","source":"schedule","created_at":"2026-09-28T03:00:00Z","updated_at":"2026-09-28T03:10:00Z","web_url":"u1"}]`),
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSyncNormalisesAProject(t *testing.T) {
	forge := providertest.New(t, gitlabRoutes())
	p, err := New(Config{URL: forge.URL, Token: "t", TokenAccounts: []string{"acme"}}, providertest.Store(t), forge.Client(), discard())
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Sync(context.Background(), provider.Account{Login: "acme"}, provider.RepoRef{ID: "gitlab:42", FullName: "acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "gitlab:42" || r.Language != "Go" || r.Visibility != "internal" || r.OpenPRs != 1 || len(r.Releases) != 0 {
		t.Fatalf("project = %+v; releases switched off read as none", r)
	}
	if r.Dependabot == nil || r.Dependabot.Critical != 1 || r.CodeScanning != nil {
		t.Errorf("security = %+v %+v; code counts need a code scanner", r.Dependabot, r.CodeScanning)
	}
	if r.PullRequests[0].Checks != provider.ChecksFailure || r.Issues[0].Number != 9 || r.Issues[0].Comments != 1 {
		t.Errorf("merge requests %+v issues %+v", r.PullRequests, r.Issues)
	}
	if d := r.Deployments[0]; d.Status != provider.DeploySucceeded || d.FinishedAt == nil || d.FinishedAt.Format("15:04") != "01:04" || d.Environment != "production" {
		t.Errorf("deployment = %+v", d)
	}
	f := r.Facts
	if !*f.BranchProtected || f.RequiresReview != nil || !*f.HasReadme || !*f.HasCodeowners || f.LastCommitterEmail != "dev@example.com" {
		t.Errorf("facts = %+v; approval rules a free tier refuses stay unknown", f)
	}
	if len(r.Pipelines) != 2 {
		t.Fatalf("pipelines = %d, want the CI pipeline and one schedule", len(r.Pipelines))
	}
	ci, nightly := r.Pipelines[0], r.Pipelines[1]
	if len(ci.Runs) != 2 || ci.Runs[1].Trigger != provider.TriggerTag {
		t.Errorf("ci runs = %+v; scheduled runs live on their schedule", ci.Runs)
	}
	if nightly.ID != "schedule:11" || nightly.Schedules[0].TimeZone != "America/New_York" || len(nightly.Runs) != 1 {
		t.Errorf("schedule = %+v", nightly)
	}
}

func TestWebhookChecksTheToken(t *testing.T) {
	p := &Provider{cfg: Config{WebhookSecret: "s3cret"}}
	body := []byte(`{"project":{"path_with_namespace":"acme/api"}}`)
	d, err := p.Webhook(context.Background(), http.Header{"X-Gitlab-Token": {"s3cret"}, "X-Gitlab-Event": {"Push Hook"}}, body)
	if err != nil || !reflect.DeepEqual(d.Repos, []string{"acme/api"}) {
		t.Fatalf("delivery %+v %v", d, err)
	}
	if _, err := p.Webhook(context.Background(), http.Header{"X-Gitlab-Token": {"guess"}}, body); err == nil {
		t.Fatal("a wrong token was taken")
	}
}

func TestOAuthRotatesAndSealsItsTokens(t *testing.T) {
	refreshes := 0
	forge := providertest.New(t, map[string]providertest.Route{
		"GET /api/v4/user": {Body: `{"id":1,"username":"dev"}`},
	})
	forge.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			refreshes++
			if r.Form.Get("refresh_token") != map[int]string{1: "seed", 2: "rotated-1"}[refreshes] {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"a` + string(rune('0'+refreshes)) + `","refresh_token":"rotated-` + string(rune('0'+refreshes)) + `","expires_in":1}`))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer a") {
			http.Error(w, "no", 401)
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"username":"dev"}`))
	})
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	store := providertest.Store(t)
	p, err := New(Config{URL: forge.URL, ClientID: "c", ClientSecret: "s", RefreshToken: "seed", SecretKey: key}, store, forge.Client(), discard())
	if err != nil {
		t.Fatal(err)
	}
	f := p.client()
	if _, err := one[glUser](context.Background(), f, p.api("user", nil)); err != nil {
		t.Fatal(err)
	}
	var kept Tokens
	if ok, _ := store.Setting(context.Background(), tokenSetting, &kept); !ok || kept.RefreshToken == "rotated-1" || kept.RefreshToken == "" {
		t.Fatalf("the rotated refresh token must be kept, sealed: %+v", kept)
	}
	// The access token lives a second, so the next call refreshes with the rotated token, not the seed.
	p2, _ := New(Config{URL: forge.URL, ClientID: "c", ClientSecret: "s", RefreshToken: "seed", SecretKey: key}, store, forge.Client(), discard())
	if _, err := one[glUser](context.Background(), p2.client(), p2.api("user", q("x", "1"))); err != nil {
		t.Fatalf("a restart starts from the kept token: %v", err)
	}
	if refreshes != 2 {
		t.Fatalf("refreshes = %d, want 2", refreshes)
	}
}
