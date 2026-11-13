package azuredevops

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
)

func testProvider(t *testing.T, routes map[string]providertest.Route) (*Provider, *providertest.Forge) {
	t.Helper()
	forge := providertest.New(t, routes)
	p := New(Config{Root: forge.URL, Security: forge.URL, Login: forge.URL, TenantID: "tenant", ClientID: "c", ClientSecret: "s", Organizations: []string{"acme"}}, providertest.Store(t), forge.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, forge
}

func TestDeploymentsComeFromEnvironmentRecords(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	record := func(id, build int64, result string, queued time.Duration) string {
		finish := fmt.Sprintf(`,"finishTime":"%s"`, at(queued-10*time.Minute))
		if result == "" {
			finish = ""
		}
		return fmt.Sprintf(`{"id":%d,"result":%q,"queueTime":"%s","startTime":"%s"%s,"owner":{"id":%d,"_links":{"web":{"href":"https://dev.azure.com/b/%d"}}}}`,
			id, result, at(queued), at(queued-time.Minute), finish, build, build)
	}
	const records = "GET /acme/p1/_apis/distributedtask/environments/%d/environmentdeploymentrecords?api-version=7.1%s&top=500"
	routes := adoRoutes()
	routes["GET /acme/p1/_apis/distributedtask/environments"] = providertest.Route{Body: `{"value":[{"id":3,"name":"Test"},{"id":20,"name":"Prod"},{"id":9,"name":"Hidden"}]}`}
	routes[fmt.Sprintf(records, 3, "")] = providertest.Route{Header: map[string]string{"X-Ms-Continuationtoken": "p2"},
		Body: `{"value":[` + strings.Join([]string{record(15, 104, "", time.Hour), record(14, 100, "succeeded", 2*time.Hour), record(13, 100, "failed", 3*time.Hour), record(12, 101, "succeeded", 4*time.Hour)}, ",") + `]}`}
	routes[fmt.Sprintf(records, 3, "&continuationToken=p2")] = providertest.Route{Header: map[string]string{"X-Ms-Continuationtoken": "p3"},
		Body: `{"value":[` + strings.Join([]string{record(11, 102, "succeeded", 5*time.Hour), record(10, 100, "canceled", 6*time.Hour), record(9, 100, "succeeded", 300*24*time.Hour)}, ",") + `]}`}
	routes[fmt.Sprintf(records, 20, "")] = providertest.Route{Body: `{"value":[` + record(16, 100, "succeededWithIssues", 30*time.Minute) + `]}`}
	routes[fmt.Sprintf(records, 9, "")] = providertest.Route{Status: 403, Body: `{"message":"no access"}`}
	routes["GET /acme/p1/_apis/build/builds?api-version=7.1&buildIds=100%2C101%2C102%2C104"] = providertest.Route{Body: `{"value":[
		{"id":100,"sourceBranch":"refs/heads/main","sourceVersion":"abc","repository":{"id":"R1"},"requestedFor":{"displayName":"A Developer"}},
		{"id":101,"sourceBranch":"refs/heads/main","sourceVersion":"def","repository":{"id":"r2"}},
		{"id":104,"sourceBranch":"refs/heads/hotfix","sourceVersion":"fed","repository":{"id":"r1"}}]}`}
	p, forge := testProvider(t, routes)
	account := provider.Account{Login: "acme"}

	r, err := p.Sync(context.Background(), account, provider.RepoRef{ID: "azuredevops:r1"})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, environment, status, sha, ref string
		finished                          bool
	}
	var got []row
	for _, d := range r.Deployments {
		got = append(got, row{d.ID, d.Environment, d.Status, d.SHA, d.Ref, d.FinishedAt != nil})
	}
	want := []row{
		{"16", "Prod", provider.DeploySucceeded, "abc", "main", true},
		{"15", "Test", provider.DeployRunning, "fed", "hotfix", false},
		{"14", "Test", provider.DeploySucceeded, "abc", "main", true},
		{"13", "Test", provider.DeployFailed, "abc", "main", true},
		{"10", "Test", provider.DeployOther, "abc", "main", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deployments = %+v\nwant %+v\nanother repository's, a purged build's, and one before the window are left out", got, want)
	}
	if d := r.Deployments[2]; d.URL != "https://dev.azure.com/b/100" || d.Actor != "A Developer" || !d.CreatedAt.Equal(now.Add(-2*time.Hour+time.Minute)) || !d.FinishedAt.Equal(now.Add(-2*time.Hour+10*time.Minute)) {
		t.Errorf("deployment = %+v; it carries its build's page and who asked, and the job's own times", d)
	}

	reads := func(part string) int {
		n := 0
		for _, req := range forge.Requests {
			if strings.Contains(req, part) {
				n++
			}
		}
		return n
	}
	if reads("continuationToken=p3") != 0 {
		t.Error("paging went on past the window")
	}
	if _, err := p.Sync(context.Background(), account, provider.RepoRef{ID: "azuredevops:r1"}); err != nil {
		t.Fatal(err)
	}
	if n := reads("/distributedtask/environments?"); n != 1 {
		t.Errorf("environments read %d times; one read answers for a project's repositories", n)
	}
}

func TestCommitsBetweenAreOldestFirst(t *testing.T) {
	const commits = "/acme/_apis/git/repositories/r1/commits"
	p, _ := testProvider(t, map[string]providertest.Route{
		"POST /tenant/oauth2/v2.0/token": {Body: `{"access_token":"tok","expires_in":3600}`},
		"GET " + commits + "?api-version=7.1&searchCriteria.%24top=250&searchCriteria.compareVersion.version=head&searchCriteria.compareVersion.versionType=commit&searchCriteria.itemVersion.version=base&searchCriteria.itemVersion.versionType=commit": {Body: `{"value":[
			{"commitId":"head","comment":"fix: mend it\n\nlonger words","author":{"date":"2026-09-30T02:00:00Z"}},
			{"commitId":"mid","comment":"feat: add it","author":{"date":"2026-09-30T01:00:00Z"}}]}`},
		"GET " + commits + "/head": {Body: `{"commitId":"head","comment":"fix: mend it","committer":{"date":"2026-09-30T02:00:00Z"}}`},
	})
	account, repo := provider.Account{Login: "acme"}, &provider.Repo{ID: "azuredevops:r1", FullName: "acme/Home/site"}
	got, err := p.CommitsBetween(context.Background(), account, repo, "base", "head")
	want := []provider.Commit{{SHA: "mid", AuthoredAt: parse("2026-09-30T01:00:00Z"), Title: "feat: add it"}, {SHA: "head", AuthoredAt: parse("2026-09-30T02:00:00Z"), Title: "fix: mend it"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("commits = %+v, %v; want %+v", got, err, want)
	}
	if got, err = p.CommitsBetween(context.Background(), account, repo, "", "head"); err != nil || len(got) != 1 || got[0].SHA != "head" {
		t.Errorf("first deployment = %+v, %v; it carries its own commit", got, err)
	}
	if got, err = p.CommitsBetween(context.Background(), account, repo, "lost", "head"); err != nil || got != nil {
		t.Errorf("a base the forge no longer has = %+v, %v; want unknown", got, err)
	}
}
