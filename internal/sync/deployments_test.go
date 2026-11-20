package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/outcomes"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/provider/providertest"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/telemetry"
)

// estate is a fake hub: it keeps every entity written, whole, answers list
// reads from canned entities by blueprint, and notes each delete.
type estate struct {
	*httptest.Server
	mu      gosync.Mutex
	written map[string]sdk.Entity
	deleted []string
	lists   map[string][]map[string]any
}

func newEstate(t *testing.T) *estate {
	t.Helper()
	h := &estate{written: map[string]sdk.Entity{}, lists: map[string][]map[string]any{}}
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.Close)
	return h
}

func (h *estate) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPut && p == "/v1/ingest/blueprints":
		_, _ = w.Write([]byte(`{"created":[],"extended":[],"kept":[],"rejected":[]}`))
	case r.Method == http.MethodPost && p == "/v1/ingest/runs":
		_, _ = w.Write([]byte(`{"id":"run","sightings":false}`))
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/entities"):
		var in struct{ Entities []sdk.Entity }
		_ = json.Unmarshal(body, &in)
		for _, e := range in.Entities {
			h.written[e.Key] = e
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(in.Entities), "rejected": []any{}})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/commit"):
		_, _ = w.Write([]byte(`{"id":"run","state":"committed","counts":{}}`))
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/v1/ingest/entities/"):
		parts := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/ingest/entities/"), "/", 2)
		key, _ := url.PathUnescape(parts[1])
		h.deleted = append(h.deleted, key)
		delete(h.written, key)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && p == "/v1/entities":
		list := h.lists[r.URL.Query().Get("blueprint")]
		if list == nil {
			list = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entities": list, "total": len(list)})
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

// service is the service relation written on a deployment, "" for none.
func (h *estate) service(t *testing.T, key string) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.written[key]
	if !ok {
		t.Fatalf("no deployment %s written; written: %v", key, h.keys())
	}
	s, _ := e.Relations["service"].(string)
	return s
}

func (h *estate) keys() []string {
	var out []string
	for k := range h.written {
		out = append(out, k)
	}
	return out
}

// forge is a fake forge: the changed files and the commits it answers per
// range, the repository a sync reads, and the files at its head.
type forge struct {
	paths map[string][]string
	repo  *provider.Repo
	files map[string]string
}

func (f *forge) Kind() provider.Kind                                                 { return provider.GitLab }
func (f *forge) Configured() bool                                                    { return true }
func (f *forge) Accounts(context.Context) ([]provider.Account, error)                { return nil, nil }
func (f *forge) Quotas() []telemetry.Quota                                           { return nil }
func (f *forge) Repos(context.Context, provider.Account) ([]provider.RepoRef, error) { return nil, nil }
func (f *forge) Sync(context.Context, provider.Account, provider.RepoRef) (*provider.Repo, error) {
	return f.repo, nil
}
func (f *forge) Files(context.Context, provider.Account, *provider.Repo) (services.Files, error) {
	if f.files == nil {
		return nil, nil
	}
	return fakeFiles(f.files), nil
}
func (f *forge) CommitsBetween(_ context.Context, _ provider.Account, _ *provider.Repo, _, head string) ([]provider.Commit, error) {
	return []provider.Commit{{SHA: head, AuthoredAt: time.Now().Add(-48 * time.Hour), Title: "feat: a change"}}, nil
}
func (f *forge) PathsBetween(_ context.Context, _ provider.Account, _ *provider.Repo, base, head string) ([]string, error) {
	return f.paths[base+".."+head], nil
}

type fakeFiles map[string]string

func (f fakeFiles) Commit() string { return "head" }
func (f fakeFiles) List(context.Context) ([]string, error) {
	var out []string
	for k := range f {
		out = append(out, k)
	}
	return out, nil
}
func (f fakeFiles) Read(_ context.Context, path string) ([]byte, error) {
	if s, ok := f[path]; ok {
		return []byte(s), nil
	}
	return nil, nil
}
func (f fakeFiles) Commits(context.Context, string) ([]services.Commit, error) { return nil, nil }

// deployEngine is an engine writing to h through f, reading the hub when read is set.
func deployEngine(t *testing.T, h *estate, f *forge, read bool) *Engine {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tel, err := telemetry.Setup(ctx, "", "", "test", log)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Providers: []provider.Provider{f}, Store: providertest.Store(t), Push: sdk.New(h.URL, "token", sdk.WithLogger(log)), Tel: tel, Log: log, GitLabHost: "gitlab.example.com"}
	if read {
		r := outcomes.NewReader(h.URL, "token", nil, h.Client(), log)
		o.Read = &r
	}
	withComponents(&o, "deploy_component")
	return New(o)
}

var account = provider.Account{ID: "1", Provider: provider.GitLab, Login: "example"}

// track holds a repository as read, with its services.
func track(e *Engine, fullName string, found *services.Found) (string, *provider.Repo) {
	repo := &provider.Repo{ID: "7", Provider: provider.GitLab, AccountLogin: "example", Name: fullName[strings.LastIndex(fullName, "/")+1:], FullName: fullName,
		URL: "https://gitlab.example.com/" + fullName, DefaultBranch: "main"}
	key := catalog.RepoKey(repo.Provider, repo.FullName)
	e.repos[key] = &tracked{account: account, ref: provider.RepoRef{ID: repo.ID, FullName: repo.FullName}, repo: repo, found: found, ok: true}
	return key, repo
}

// keep stores a forge deployment as a sync keeps it, in the state's own shape.
func keep(t *testing.T, e *Engine, repoKey, id, env, job, status, sha string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago).UTC()
	facts := map[string]any{"key": repoKey + "/" + id, "source": "forge", "repoKey": repoKey, "folder": "", "environment": env, "status": status, "revision": sha, "finishedAt": at,
		"deployment": map[string]any{"id": id, "environment": env, "status": status, "sha": sha, "createdAt": at.Add(-time.Minute), "finishedAt": at, "job": job}}
	if err := e.o.Store.PutDeployment(context.Background(), repoKey+"/"+id, repoKey, at, facts); err != nil {
		t.Fatal(err)
	}
}

// monorepo is a repository holding an orders and a billing service, each
// naming its deploy component in its orrery.yaml.
func monorepo(repoKey string) *services.Found {
	return &services.Found{
		Services: []services.Service{{ID: "services/orders", Path: "services/orders", Name: "orders", Type: "backend"}, {ID: "services/billing", Path: "services/billing", Name: "billing", Type: "backend"}},
		Descriptions: []services.Description{
			{Descriptor: services.Descriptor{Service: repoKey + "/services/orders", Fields: map[string]any{"deploy_component": "orders"}}, File: "services/orders/orrery.yaml"},
			{Descriptor: services.Descriptor{Service: repoKey + "/services/billing", Fields: map[string]any{"deploy_component": "billing"}}, File: "services/billing/orrery.yaml"},
		},
	}
}

func TestAMonorepoDeploymentGoesToTheServiceItsJobOrEnvironmentNames(t *testing.T) {
	h := newEstate(t)
	e := deployEngine(t, h, &forge{}, false)
	key, _ := track(e, "example/shop", nil)
	e.repos[key].found = monorepo(key)
	keep(t, e, key, "101", "production", "deploy/deploy:orders", "succeeded", "a1", 3*time.Hour)
	keep(t, e, key, "102", "billing-production", "", "succeeded", "a1", 3*time.Hour)
	keep(t, e, key, "103", "production", "deploy/smoke-test", "succeeded", "a1", 3*time.Hour)

	e.pushOutcomes(context.Background())

	if got := h.service(t, key+"/101"); got != key+"/services/orders" {
		t.Errorf("the job deploy:orders went to %q", got)
	}
	if got := h.service(t, key+"/102"); got != key+"/services/billing" {
		t.Errorf("the environment billing-production went to %q", got)
	}
	if got := h.service(t, key+"/103"); got != "" {
		t.Errorf("a job naming no component went to %q; it stays unplaced", got)
	}
}

func TestAMonorepoDeploymentGoesToTheServicesItsChangesTouch(t *testing.T) {
	h := newEstate(t)
	f := &forge{paths: map[string][]string{
		"..b1":   {"Api/Program.cs"},
		"b1..b2": {"Api/Controllers/Orders.cs", "Web/src/app.ts", "README.md"},
	}}
	e := deployEngine(t, h, f, false)
	key, _ := track(e, "example/portal", &services.Found{Services: []services.Service{{ID: "api", Path: "Api", Name: "Api"}, {ID: "web", Path: "Web", Name: "Web"}}})
	keep(t, e, key, "200", "Test", "", "succeeded", "b1", 5*time.Hour)
	// One build, two jobs against one environment, neither named for a service.
	keep(t, e, key, "201", "Test", "Deploy/Job_1", "succeeded", "b2", 2*time.Hour)
	keep(t, e, key, "202", "Test", "Deploy/Job_2", "failed", "b2", 2*time.Hour-time.Minute)

	e.pushOutcomes(context.Background())

	if got := h.service(t, key+"/200"); got != key+"/api" {
		t.Errorf("a revision that changed only Api went to %q", got)
	}
	if got := h.service(t, key+"/201"); got != key+"/api" {
		t.Errorf("the build's first deployment carries its first service by key, got %q", got)
	}
	if got := h.service(t, key+"/201@web"); got != key+"/web" {
		t.Errorf("the build's second service is a record of its own, got %q", got)
	}
	if got := h.service(t, key+"/202"); got != "" {
		t.Errorf("the build's other job is not counted twice, got %q", got)
	}
	h.mu.Lock()
	status := h.written[key+"/201@web"].Fields["status"]
	h.mu.Unlock()
	if status != provider.DeployFailed {
		t.Errorf("a build whose jobs did not all deploy is a failed deployment of each service, got %v", status)
	}

	// Web leaves the repository: its record is no longer made, and is deleted.
	e.repos[key].found = &services.Found{Services: []services.Service{{ID: "api", Path: "Api", Name: "Api"}}}
	e.pushOutcomes(context.Background())
	h.mu.Lock()
	deleted := h.deleted
	h.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != key+"/201@web" {
		t.Errorf("deleted %v, want the record for the service that left", deleted)
	}
}

func TestAKeptDeploymentIsPlacedOnTheNextPass(t *testing.T) {
	h := newEstate(t)
	e := deployEngine(t, h, &forge{}, false)
	key, _ := track(e, "example/shop", nil)
	keep(t, e, key, "300", "production", "deploy/deploy:billing", "succeeded", "c1", 40*24*time.Hour)

	e.pushOutcomes(context.Background())
	if got := h.service(t, key+"/300"); got != "" {
		t.Fatalf("placed before the services were read: %q", got)
	}

	e.repos[key].found = monorepo(key)
	e.pushOutcomes(context.Background())
	if got := h.service(t, key+"/300"); got != key+"/services/billing" {
		t.Errorf("a deployment kept 40 days went to %q on the next pass", got)
	}
}

// argoHub lists Argo CD apps, each with one finished sync and no workload,
// and holds each sync as the cluster pusher wrote it.
func argoHub(h *estate, apps map[string][2]string) {
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, source := range apps {
		h.lists["argocd_app"] = append(h.lists["argocd_app"], map[string]any{"key": key, "fields": map[string]any{"repo_url": source[0], "path": source[1]}, "relations": map[string]any{}})
		h.lists["sw_deployment"] = append(h.lists["sw_deployment"], map[string]any{"key": key + "/1", "fields": map[string]any{"status": "succeeded", "revision": "m1", "deployed_at": at},
			"relations": map[string]any{"app": []any{map[string]any{"key": key}}}})
	}
}

func TestAnArgoCDSyncWithNoWorkloadGoesToTheServiceFolderItsSourceSitsIn(t *testing.T) {
	h := newEstate(t)
	e := deployEngine(t, h, &forge{}, true)
	platformKey, _ := track(e, "example/platform", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "platform"}, {ID: "services/orders", Path: "services/orders", Name: "orders"}}})
	track(e, "ops/gitops", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "gitops", Type: "infrastructure"}}})
	argoHub(h, map[string][2]string{
		"c/argocd/orders":  {"https://gitlab.example.com/example/platform", "services/orders/deploy"},
		"c/argocd/ingress": {"https://gitlab.example.com/ops/gitops", "cluster/ingress"},
	})

	e.pushOutcomes(context.Background())

	if got := h.service(t, "c/argocd/orders/1"); got != platformKey+"/services/orders" {
		t.Errorf("a sync whose source sits in the orders folder went to %q", got)
	}
	h.mu.Lock()
	_, ingress := h.written["c/argocd/ingress/1"]
	h.mu.Unlock()
	if ingress {
		t.Error("a sync whose source sits in a repository's root went to its root service; a root holds every folder")
	}
}

func TestAnArgoCDSyncGoesToTheServiceThatNamesItsSource(t *testing.T) {
	h := newEstate(t)
	e := deployEngine(t, h, &forge{}, true)
	shopKey, _ := track(e, "example/shop", nil)
	declared, err := services.ParseDescriptor([]byte("service: "+shopKey+"\ndeploys:\n  argocd:\n  - https://gitlab.example.com/ops/gitops/-/tree/main/apps/shop\n"), shopKey)
	if err != nil {
		t.Fatalf("orrery.yaml refused: %v", err)
	}
	e.repos[shopKey].found = &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "shop"}}, Descriptions: []services.Description{{Descriptor: *declared, File: "orrery.yaml"}}}
	track(e, "ops/gitops", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "gitops", Type: "infrastructure"}}})
	argoHub(h, map[string][2]string{"c/argocd/shop-prod": {"https://gitlab.example.com/ops/gitops", "apps/shop/envs/prod"}})

	e.pushOutcomes(context.Background())

	if got := h.service(t, "c/argocd/shop-prod/1"); got != shopKey {
		t.Errorf("a sync of the source shop's orrery.yaml names went to %q", got)
	}
	h.mu.Lock()
	repo := h.written["c/argocd/shop-prod/1"].Relations["repository"]
	h.mu.Unlock()
	if repo != shopKey {
		t.Errorf("the sync's repository is %v, want the service's", repo)
	}
}

func TestAnArgoCDSyncGoesToTheServiceTheOperatorLinksItTo(t *testing.T) {
	h := newEstate(t)
	e := deployEngine(t, h, &forge{}, true)
	var logs bytes.Buffer
	e.o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	toolKey, _ := track(e, "vendor/tool", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "tool"}}})
	platformKey, _ := track(e, "example/platform", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "platform"}, {ID: "services/orders", Path: "services/orders", Name: "orders"}}})
	shopKey, _ := track(e, "example/shop", nil)
	declared, err := services.ParseDescriptor([]byte("service: "+shopKey+"\ndeploys:\n  argocd:\n  - https://gitlab.example.com/ops/gitops/-/tree/main/apps/shop\n"), shopKey)
	if err != nil {
		t.Fatalf("orrery.yaml refused: %v", err)
	}
	e.repos[shopKey].found = &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "shop"}}, Descriptions: []services.Description{{Descriptor: *declared, File: "orrery.yaml"}}}
	track(e, "ops/gitops", &services.Found{Services: []services.Service{{ID: "", Path: "", Name: "gitops", Type: "infrastructure"}}})
	argoHub(h, map[string][2]string{
		// The tool runs on two clusters from a chart: its name places both.
		"c1/argocd/tool": {"https://charts.example.com", ""},
		"c2/argocd/tool": {"https://charts.example.com", ""},
		// A folder of the GitOps repository holds the orders app's manifests.
		"c1/argocd/orders-prod": {"https://gitlab.example.com/ops/gitops", "apps/orders/envs/prod"},
		// shop's own orrery.yaml names this source; the operator's link says otherwise.
		"c1/argocd/shop": {"https://gitlab.example.com/ops/gitops", "apps/shop/envs/prod"},
		// A link names this app's service, which no watched repository holds.
		"c1/argocd/ghost": {"https://charts.example.com", ""},
	})
	// A sync kept 150 days is placed by the same pass.
	h.mu.Lock()
	h.lists["sw_deployment"] = append(h.lists["sw_deployment"], map[string]any{"key": "c1/argocd/tool/old", "fields": map[string]any{"status": "succeeded", "revision": "m0",
		"deployed_at": time.Now().Add(-150 * 24 * time.Hour).UTC().Format(time.RFC3339)}, "relations": map[string]any{"app": []any{map[string]any{"key": "c1/argocd/tool"}}}})
	h.mu.Unlock()
	e.o.ArgoServices = map[string]string{
		"tool": toolKey,
		"https://gitlab.example.com/ops/gitops/-/tree/main/apps/orders": platformKey + "/services/orders",
		"c1/argocd/shop":    platformKey,
		"c1/argocd/missing": toolKey,
		"c1/argocd/ghost":   "gitlab/vendor/nothing",
	}

	e.pushOutcomes(context.Background())

	for key, want := range map[string]string{
		"c1/argocd/tool/1":        toolKey,
		"c2/argocd/tool/1":        toolKey,
		"c1/argocd/tool/old":      toolKey,
		"c1/argocd/orders-prod/1": platformKey + "/services/orders",
		"c1/argocd/shop/1":        platformKey,
	} {
		if got := h.service(t, key); got != want {
			t.Errorf("%s went to %q, want %q", key, got, want)
		}
	}
	h.mu.Lock()
	repo := h.written["c1/argocd/tool/1"].Relations["repository"]
	_, ghost := h.written["c1/argocd/ghost/1"]
	h.mu.Unlock()
	if repo != toolKey {
		t.Errorf("the linked sync's repository is %v, want the service's", repo)
	}
	if ghost {
		t.Error("a link to a service no watched repository holds placed a sync")
	}
	out := logs.String()
	for _, want := range []string{`"app":"c1/argocd/shop"`, `"declared":"` + shopKey + `"`, `"service":"gitlab/vendor/nothing"`, `"link":"c1/argocd/missing"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the logs do not say %s:\n%s", want, out)
		}
	}
}

func TestAServiceThatDeploysByItsPipelineGetsADeploymentPerRevision(t *testing.T) {
	h := newEstate(t)
	now := time.Now().UTC()
	run := func(id, trigger, ref, sha, conclusion string, ago time.Duration) provider.Run {
		return provider.Run{ID: id, Trigger: trigger, Ref: ref, SHA: sha, Status: provider.StatusCompleted, Conclusion: conclusion, CreatedAt: now.Add(-ago - 10*time.Minute), UpdatedAt: now.Add(-ago), URL: "https://gitlab.example.com/example/pusher/-/pipelines/" + id}
	}
	repo := &provider.Repo{ID: "7", Provider: provider.GitLab, AccountLogin: "example", Name: "pusher", FullName: "example/pusher", URL: "https://gitlab.example.com/example/pusher", DefaultBranch: "main",
		Pipelines: []provider.Pipeline{{ID: "ci", Name: "Pipeline", Path: ".gitlab-ci.yml", State: provider.Enabled, Runs: []provider.Run{
			run("r6", provider.TriggerTag, "v1.0.0", "s5", provider.Success, 1*time.Hour),
			run("r5", provider.TriggerPush, "main", "s4", provider.Failure, 2*time.Hour),
			run("r4", provider.TriggerPush, "feature", "s3", provider.Success, 3*time.Hour),
			run("r3", provider.TriggerSchedule, "main", "s2", provider.Success, 4*time.Hour),
			run("r2", provider.TriggerManual, "main", "s1", provider.Success, 5*time.Hour),
			run("r1", provider.TriggerPush, "main", "s1", provider.Success, 6*time.Hour),
		}}}}
	key := catalog.RepoKey(repo.Provider, repo.FullName)
	f := &forge{repo: repo, files: map[string]string{
		"orrery.yaml": "service: " + key + "\ndeploys:\n  pipeline: .gitlab-ci.yml\n",
		"go.mod":      "module example.com/pusher\n", "main.go": "package main\nfunc main() {}\n",
	}}
	e := deployEngine(t, h, f, false)
	e.repos[key] = &tracked{account: account, ref: provider.RepoRef{ID: repo.ID, FullName: repo.FullName}}
	if err := e.syncRepo(context.Background(), key); err != nil {
		t.Fatal(err)
	}

	e.pushOutcomes(context.Background())

	h.mu.Lock()
	var got []string
	for k, x := range h.written {
		got = append(got, k)
		if x.Relations["service"] != key || x.Fields["status"] != provider.DeploySucceeded || x.Fields["environment_name"] != "production" {
			t.Errorf("%s = %+v", k, x)
		}
	}
	h.mu.Unlock()
	want := map[string]bool{key + "@run-r1": true, key + "@run-r6": true}
	if len(got) != len(want) || !want[got[0]] || !want[got[1]] {
		t.Errorf("deployments %v; want the first good default-branch run of each revision and the tag's", got)
	}
}

func TestOnlyKeysOfRedgreensOwnShapeAreRemoved(t *testing.T) {
	repo := "gitlab/example/shop"
	for key, want := range map[string]bool{
		repo + "/201@services/web":     true,
		repo + "@run-77":               true,
		repo + "/services/jobs@run-77": true,
		repo + "/ci/job/7@elsewhere":   false,
		repo + "/201":                  false,
		repo + "-other/1@x":            false,
	} {
		owner := ownerOf(key, map[string]bool{repo: true, repo + "/sub": true})
		if got := owner == repo && ownShape(key, owner); got != want {
			t.Errorf("%s removable = %v, want %v", key, got, want)
		}
	}
}
