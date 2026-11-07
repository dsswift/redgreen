package azuredevops

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
)

func TestRefsRunsAndTriggers(t *testing.T) {
	for in, want := range map[string]string{"refs/heads/release/1.0": "release/1.0", "refs/tags/v1.2.0": "v1.2.0", "refs/pull/12/merge": "pull/12/merge"} {
		if got := shortRef(in); got != want {
			t.Errorf("shortRef(%s) = %s", in, got)
		}
	}
	for _, c := range []struct{ status, result, s, c string }{
		{"completed", "failed", "completed", "failure"}, {"completed", "partiallySucceeded", "completed", "success"},
		{"completed", "canceled", "completed", "cancelled"}, {"inProgress", "", "running", ""}, {"notStarted", "", "queued", ""},
	} {
		if s, cc := runStatus(c.status, c.result); s != c.s || cc != c.c {
			t.Errorf("%s/%s = %s/%s", c.status, c.result, s, cc)
		}
	}
	for _, c := range []struct{ reason, ref, want string }{
		{"individualCI", "refs/tags/v1", "tag"}, {"batchedCI", "refs/heads/main", "push"}, {"pullRequest", "refs/pull/3/merge", "pull_request"},
		{"schedule", "refs/heads/main", "schedule"}, {"manual", "refs/heads/main", "manual"}, {"buildCompletion", "refs/heads/main", "other"},
	} {
		if got := trigger(c.reason, c.ref); got != c.want {
			t.Errorf("trigger %s = %s", c.reason, got)
		}
	}
}

func TestSchedules(t *testing.T) {
	for in, want := range map[any]string{float64(31): "1,2,3,4,5", float64(64): "0", float64(127): "*", "all": "*", "monday, friday": "1,5"} {
		if got := cronDays(in); got != want {
			t.Errorf("cronDays(%v) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"Central Standard Time": "America/Chicago", "UTC": "Etc/UTC", "Nowhere Standard Time": "UTC"} {
		if got := ianaTimeZone(in); got != want {
			t.Errorf("zone %s = %s", in, got)
		}
	}
	s := adoDefinitionSchedule{TimeZoneID: "Central Standard Time", StartHours: 5, StartMinutes: 30, DaysToBuild: float64(31)}
	d := adoDefinition{}
	d.Triggers = append(d.Triggers, struct {
		TriggerType string                  `json:"triggerType"`
		Schedules   []adoDefinitionSchedule `json:"schedules"`
	}{TriggerType: "schedule", Schedules: []adoDefinitionSchedule{s, {ScheduleOnlyWithChanges: true}}})
	got := definitionSchedules(d)
	if got == nil || got.Conditional != 1 || !reflect.DeepEqual(got.Schedules, []provider.Schedule{{Cron: "30 5 * * 1,2,3,4,5", TimeZone: "America/Chicago"}}) {
		t.Errorf("editor schedules = %+v", got)
	}
	if definitionSchedules(adoDefinition{}) != nil {
		t.Error("a definition with no editor schedules leaves the file to decide")
	}
	file := ParseSchedules("schedules:\n  - cron: \"0 2 * * *\"\n    always: true\n  - cron: \"0 6 * * 1\"\n    branches: { include: [main] }\ntrigger: [main]\n")
	if file.Conditional != 1 || !reflect.DeepEqual(file.Schedules, []provider.Schedule{{Cron: "0 2 * * *", TimeZone: "UTC"}}) {
		t.Errorf("file schedules = %+v", file)
	}
	if bad := ParseSchedules(": not yaml :\n  - ["); len(bad.Schedules) != 0 || bad.Conditional != 0 {
		t.Errorf("broken yaml gave %+v", bad)
	}
}

func TestChecksFromTheNewestBuildOfEachPipeline(t *testing.T) {
	b := func(def int64, queued, status, result string) adoBuild {
		x := adoBuild{Status: status, Result: result}
		x.Definition.ID = def
		x.QueueTime = parse(queued)
		return x
	}
	if got := checksState([]adoBuild{b(1, "2026-09-30T02:00:00Z", "completed", "succeeded"), b(1, "2026-09-30T01:00:00Z", "completed", "failed")}); got != provider.ChecksSuccess {
		t.Errorf("a newer pass over an older failure = %s", got)
	}
	if got := checksState([]adoBuild{b(1, "2026-09-30T02:00:00Z", "inProgress", ""), b(2, "2026-09-30T02:00:00Z", "completed", "failed")}); got != provider.ChecksFailure {
		t.Errorf("one pipeline failing = %s", got)
	}
	if got := checksState(nil); got != provider.ChecksNone {
		t.Errorf("no builds = %s", got)
	}
}

func adoRoutes() map[string]providertest.Route {
	b := func(s string) providertest.Route { return providertest.Route{Body: s} }
	const git = "/acme/p1/_apis/git/repositories/r1"
	return map[string]providertest.Route{
		"POST /tenant/oauth2/v2.0/token":                     b(`{"access_token":"tok","expires_in":3600}`),
		"GET /acme/_apis/git/repositories/r1":                b(`{"id":"r1","name":"site","webUrl":"https://dev.azure.com/acme/Home/_git/site","defaultBranch":"refs/heads/main","project":{"id":"p1","name":"Home","visibility":"private"}}`),
		"GET /acme/p1/_apis/projectanalysis/languagemetrics": b(`{"repositoryLanguageAnalytics":[{"id":"r1","languageBreakdown":[{"name":"C#","languagePercentage":70},{"name":"JSON"}]}]}`),
		"GET " + git + "/pushes":                             b(`{"value":[{"date":"2026-09-30T00:00:00Z"}]}`),
		"GET " + git + "/pullrequests":                       b(`{"value":[{"pullRequestId":12,"title":"Tidy","isDraft":false,"creationDate":"2026-09-20T00:00:00Z","createdBy":{"displayName":"A Developer"}}]}`),
		"GET /acme/p1/_apis/alert/repositories/r1/alerts":    {Status: 400, Body: `{"message":"Advanced Security is not enabled for this repository"}`},
		"GET /acme/p1/_apis/git/policy/configurations":       b(`{"value":[{"isEnabled":true,"isBlocking":true,"type":{"id":"fa4e907d-c16b-4a4c-9dfa-4906e5d171dd"},"settings":{"minimumApproverCount":2}}]}`),
		"GET " + git + "/items":                              b(`{"value":[{"path":"/README.md"},{"path":"/src"}]}`),
		"GET " + git + "/commits":                            b(`{"value":[{"commitId":"head","author":{"name":"A Developer","email":"dev@example.com","date":"2026-09-30T00:00:00Z"}}]}`),
		"GET /acme/p1/_apis/build/definitions":               b(`{"value":[{"id":7,"name":"CI","queueStatus":"enabled","createdDate":"2026-01-01T00:00:00Z","process":{"type":2,"yamlFilename":"azure-pipelines.yml"}},{"id":8,"name":"Draft","quality":"draft","createdDate":"2026-01-01T00:00:00Z"}]}`),
		"GET /acme/p1/_apis/build/builds":                    b(`{"value":[{"id":100,"buildNumber":"20260930.1","status":"completed","result":"failed","queueTime":"2026-09-30T01:00:00Z","finishTime":"2026-09-30T01:10:00Z","definition":{"id":7},"sourceBranch":"refs/heads/main","sourceVersion":"abc","reason":"individualCI","_links":{"web":{"href":"https://dev.azure.com/b/100"}}}]}`),
	}
}

func parse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestSyncNormalisesARepository(t *testing.T) {
	forge := providertest.New(t, adoRoutes())
	p := New(Config{Root: forge.URL, Security: forge.URL, Login: forge.URL, TenantID: "tenant", ClientID: "c", ClientSecret: "s", Organizations: []string{"https://dev.azure.com/acme/"}}, providertest.Store(t), forge.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	accounts, _ := p.Accounts(context.Background())
	if len(accounts) != 1 || accounts[0].Login != "acme" {
		t.Fatalf("accounts = %+v; an organization named by its URL reads as its name", accounts)
	}
	r, err := p.Sync(context.Background(), accounts[0], provider.RepoRef{ID: "azuredevops:r1", FullName: "acme/Home/site"})
	if err != nil {
		t.Fatal(err)
	}
	if r.FullName != "acme/Home/site" || r.Language != "C#" || r.DefaultBranch != "main" || r.Dependabot != nil || r.Issues != nil || r.Deployments != nil {
		t.Fatalf("repo = %+v; security off, and no issues or deployments read", r)
	}
	if !*r.Facts.BranchProtected || *r.Facts.RequiredApprovals != 2 || !*r.Facts.HasReadme || r.Facts.LastCommitter != "A Developer" {
		t.Errorf("facts = %+v", r.Facts)
	}
	if len(r.Pipelines) != 1 || r.Pipelines[0].Runs[0].Conclusion != provider.Failure || r.Pipelines[0].Path != "azure-pipelines.yml" {
		t.Errorf("pipelines = %+v; a draft is left out", r.Pipelines)
	}
	if r.PullRequests[0].URL != "https://dev.azure.com/acme/Home/_git/site/pullrequest/12" || r.PullRequests[0].Checks != provider.ChecksFailure {
		t.Errorf("pull requests = %+v", r.PullRequests)
	}
}

func TestASignInPageIsUnauthorized(t *testing.T) {
	routes := adoRoutes()
	routes["GET /acme/_apis/git/repositories"] = providertest.Route{Status: 203, Body: "<html>sign in</html>"}
	forge := providertest.New(t, routes)
	p := New(Config{Root: forge.URL, Login: forge.URL, TenantID: "tenant", ClientID: "c", ClientSecret: "s", Organizations: []string{"acme"}}, providertest.Store(t), forge.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := p.Repos(context.Background(), provider.Account{Login: "acme"})
	if !provider.IsStatus(err, 401) {
		t.Fatalf("err = %v, want a 401: the organization asked for a sign-in", err)
	}
}
