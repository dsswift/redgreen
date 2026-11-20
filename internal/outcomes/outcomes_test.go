package outcomes

import (
	"reflect"
	"testing"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func h(n float64) time.Time { return t0.Add(time.Duration(n * float64(time.Hour))) }

func rec(key, status string, at float64) Record {
	return Record{Key: key, Source: "forge", RepoKey: "github/acme/api", Environment: "production", Status: status, Revision: key, FinishedAt: h(at)}
}

func carried(titles ...string) *Carried {
	c := &Carried{Commits: []provider.Commit{}}
	for i, title := range titles {
		c.Commits = append(c.Commits, provider.Commit{SHA: title, AuthoredAt: h(-float64(10 * (i + 1))), Title: title})
	}
	return c
}

func TestRepairs(t *testing.T) {
	for title, want := range map[string][2]bool{
		"fix: a bug": {true, true}, "fix(api)!: break": {true, true}, "Revert \"feat: x\"": {true, true}, "revert: x": {true, true},
		"feat: x": {false, true}, "chore: release": {false, true}, "Merge branch 'main'": {false, false},
	} {
		r, ok := IsRepair(title)
		if [2]bool{r, ok} != want {
			t.Errorf("%q = %v %v, want %v", title, r, ok, want)
		}
	}
}

func TestCarries(t *testing.T) {
	lead, count, fix := Carries(rec("a", "succeeded", 0), carried("feat: x", "fix: y", "Merge branch 'b'"))
	if *lead != 20 || *count != 3 || *fix {
		t.Errorf("lead %v count %v fix %v; the median of 10, 20, and 30 hours", *lead, *count, *fix)
	}
	_, _, fix = Carries(rec("a", "succeeded", 0), carried("fix: y", "revert: z", "Merge branch 'b'"))
	if !*fix {
		t.Error("only fixes and a merge is fix only")
	}
	if lead, count, fix := Carries(rec("a", "succeeded", 0), &Carried{Unknown: true}); lead != nil || count != nil || fix != nil {
		t.Error("unknown commits give unknown facts")
	}
	if lead, count, fix := Carries(rec("a", "succeeded", 0), carried()); lead != nil || *count != 0 || fix != nil {
		t.Error("no commits: zero carried, no lead time, fix only unknown")
	}
}

func TestFailedChanges(t *testing.T) {
	records := []Record{
		rec("d1", "succeeded", 0),
		rec("d2", "failed", 5),
		rec("d3", "succeeded", 7),
		rec("d4", "succeeded", 30),
		rec("d5", "succeeded", 32),
		rec("d6", "succeeded", 100),
		rec("d7", "succeeded", 200),
		rec("d8", "succeeded", 300),
	}
	c := map[string]*Carried{"d5": carried("fix: d4 broke it"), "d8": carried("fix: late")}
	mitigated, resolved := h(103), h(110)
	incidents := []Incident{
		{RepoKey: "github/acme/api", Severity: "critical", OpenedAt: h(101), MitigatedAt: &mitigated, ResolvedAt: &resolved},
		{RepoKey: "github/acme/api", Severity: "info", OpenedAt: h(201)},
		{RepoKey: "github/acme/api", Folder: "other", Severity: "critical", OpenedAt: h(201)},
	}
	got := Classify(records, c, incidents)
	restore := func(k string) any {
		if got[k].RestoreHours == nil {
			return nil
		}
		return *got[k].RestoreHours
	}
	for k, want := range map[string][2]any{
		"d1": {false, nil}, "d2": {true, 2.0}, "d3": {false, nil}, "d4": {true, 2.0}, "d5": {false, nil},
		"d6": {true, 3.0}, "d7": {false, nil}, "d8": {false, nil},
	} {
		if g := [2]any{got[k].FailedChange, restore(k)}; !reflect.DeepEqual(g, want) {
			t.Errorf("%s: failed, restore = %v, want %v", k, g, want)
		}
	}
	unrestored := Classify([]Record{rec("x", "failed", 0)}, nil, nil)["x"]
	if !unrestored.FailedChange || unrestored.RestoreHours != nil {
		t.Errorf("a failure with nothing after it is failed and not restored: %+v", unrestored)
	}
	resolvedOnly := Classify([]Record{rec("y", "succeeded", 0)}, nil, []Incident{{RepoKey: "github/acme/api", Severity: "warning", OpenedAt: h(1), ResolvedAt: &resolved}})["y"]
	if !resolvedOnly.FailedChange || *resolvedOnly.RestoreHours != 110 {
		t.Errorf("an incident never mitigated restores when resolved: %+v", resolvedOnly)
	}
}

func TestStreamsStayApart(t *testing.T) {
	staging := rec("s", "succeeded", 1)
	staging.Environment = "staging"
	staging.Revision = "fix"
	got := Classify([]Record{rec("p", "succeeded", 0), staging}, map[string]*Carried{"s": carried("fix: x")}, nil)
	if got["p"].FailedChange {
		t.Error("a fix in staging does not fail production")
	}
}

func TestRangeOf(t *testing.T) {
	records := []Record{rec("a", "succeeded", 0), rec("b", "failed", 1), rec("c", "succeeded", 2)}
	prev := Previous(records)
	if r, ok := RangeOf(records[2], prev["c"], nil); !ok || r != (Range{Base: "a", Head: "c"}) {
		t.Errorf("range = %+v; a failed deployment is never the base", r)
	}
	if r, _ := RangeOf(records[0], prev["a"], nil); r != (Range{Head: "a"}) {
		t.Errorf("the first of a stream has no base: %+v", r)
	}
	manifest := rec("m", "succeeded", 10)
	manifest.Elsewhere = true
	releases := []provider.Release{{Tag: "v2", PublishedAt: h(9)}, {Tag: "v3-rc", PublishedAt: h(9.5), Prerelease: true}, {Tag: "v1", PublishedAt: h(-5)}, {Tag: "v9", PublishedAt: h(20)}}
	if r, ok := RangeOf(manifest, nil, releases); !ok || r.Head != "v2" {
		t.Errorf("a manifest revision stands in as the newest full release by then: %+v", r)
	}
	if _, ok := RangeOf(manifest, nil, nil); ok {
		t.Error("with no release to stand in, the range is unknown")
	}
}

func TestPreviousKeepsStreamsAndMomentsApart(t *testing.T) {
	billing := rec("x", "succeeded", 1)
	billing.Folder = "services/billing"
	same := rec("b2", "succeeded", 2)
	records := []Record{rec("a", "succeeded", 0), billing, rec("b1", "succeeded", 2), same, rec("c", "succeeded", 3)}
	prev := Previous(records)
	if prev["a"] != nil || prev["x"] != nil {
		t.Errorf("a stream's first has none: %v %v", prev["a"], prev["x"])
	}
	if prev["b1"] == nil || prev["b1"].Key != "a" || prev["b2"] == nil || prev["b2"].Key != "a" {
		t.Errorf("two that ended at one moment are not each other's: %v %v", prev["b1"], prev["b2"])
	}
	if prev["c"] == nil || prev["c"].Key != "b1" {
		t.Errorf("c follows %v", prev["c"])
	}
}

func TestParseRepoURL(t *testing.T) {
	for in, want := range map[string]RepoRef{
		"https://github.com/dsswift/redgreen":                         {provider.GitHub, "dsswift/redgreen", ""},
		"https://github.com/acme/platform/tree/main/workloads/portal": {provider.GitHub, "acme/platform", "workloads/portal"},
		"https://github.com/o/r.git":                                  {provider.GitHub, "o/r", ""},
		"https://gitlab.com/group/sub/project":                        {provider.GitLab, "group/sub/project", ""},
		"https://gitlab.com/group/project/-/tree/main/apps/api":       {provider.GitLab, "group/project", "apps/api"},
		"https://git.example.org/g/p":                                 {provider.GitLab, "g/p", ""},
		"https://dev.azure.com/Acme/Home/_git/site":                   {provider.AzureDevOps, "Acme/Home/site", ""},
	} {
		got, ok := ParseRepoURL(in, "git.example.org")
		if !ok || got != want {
			t.Errorf("%s = %+v %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "not a url", "https://charts.jetstack.io", "https://github.com/onlyowner"} {
		if _, ok := ParseRepoURL(in, "gitlab.com"); ok {
			t.Errorf("%q parsed", in)
		}
	}
	a, _ := ParseRepoURL("https://github.com/A/B/tree/main/x", "")
	b, _ := ParseRepoURL("https://github.com/a/b", "")
	if !SameRepo(a, b) || a.Key() != "github/a/b" {
		t.Error("one repository however written")
	}
}

func TestMapSyncsSaysWhyEachWasLeftOut(t *testing.T) {
	sync := func(key, app, status, at string) HubDeployment {
		var d HubDeployment
		d.Key, d.Fields.Status, d.Fields.Revision, d.Fields.DeployedAt = key, status, "abc", at
		if app != "" {
			d.Relations.App = []related{{Key: app}}
		}
		return d
	}
	app := func(key, repo, path, workload, env string) HubApp {
		var a HubApp
		a.Key, a.Fields.RepoURL, a.Fields.Path = key, repo, path
		if workload != "" {
			a.Relations.Workload = []related{{Key: workload}}
		}
		if env != "" {
			a.Relations.Environment = []related{{Key: env}}
		}
		return a
	}
	workload := func(key, repo string) HubWorkload {
		var w HubWorkload
		w.Key, w.Fields.Repo = key, repo
		return w
	}
	apps := []HubApp{
		app("home/argocd/redgreen", "https://github.com/acme/platform", "workloads/redgreen", "redgreen", "production"),
		app("home/argocd/portal", "https://github.com/acme/platform", "workloads/portal", "portal", ""),
		app("home/argocd/cert-manager", "https://charts.jetstack.io", "", "", ""),
		app("home/argocd/vaultwarden", "https://github.com/acme/platform", "workloads/vaultwarden", "vaultwarden", ""),
		app("home/argocd/petri", "https://github.com/acme/platform", "workloads/petri", "petri", ""),
		app("home/argocd/shop", "https://github.com/acme/platform", "workloads/shop/prod", "", ""),
		app("home/argocd/orders", "https://github.com/acme/orders", "deploy", "", ""),
		app("home/argocd/twice", "https://github.com/acme/platform", "workloads/twice", "", ""),
		app("home/argocd/gone", "https://github.com/acme/platform", "workloads/gone", "nowhere", ""),
	}
	workloads := []HubWorkload{
		workload("redgreen", "https://github.com/dsswift/redgreen"),
		workload("portal", "https://github.com/acme/platform/tree/main/workloads/portal"),
		workload("vaultwarden", ""),
		workload("petri", "https://github.com/dsswift/petri"),
	}
	targets := map[string][]Target{
		"github/dsswift/redgreen": {{Key: "github/dsswift/redgreen"}},
		"github/acme/platform":    {{Key: "github/acme/platform"}, {Key: "github/acme/platform/workloads/portal", Path: "workloads/portal"}},
		"github/acme/orders":      {{Key: "github/acme/orders"}, {Key: "github/acme/orders/deploy", Path: "deploy"}},
		"github/acme/shop":        {{Key: "github/acme/shop"}},
	}
	shopSource, _ := ParseRepoURL("https://github.com/acme/platform/tree/main/workloads/shop", "")
	twiceSource, _ := ParseRepoURL("https://github.com/acme/platform/tree/main/workloads/twice", "")
	places := Places{
		Declared: []Declared{
			{Source: shopSource, RepoKey: "github/acme/shop", Target: Target{Key: "github/acme/shop"}},
			{Source: twiceSource, RepoKey: "github/acme/shop", Target: Target{Key: "github/acme/shop"}},
			{Source: twiceSource, RepoKey: "github/dsswift/redgreen", Target: Target{Key: "github/dsswift/redgreen"}},
		},
		Targets: func(key string) []Target { return targets[key] },
	}
	at := "2026-10-30T10:00:05Z"
	got, m := MapSyncs([]HubDeployment{
		sync("home/argocd/redgreen/3", "home/argocd/redgreen", "succeeded", at),
		sync("home/argocd/portal/9", "home/argocd/portal", "failed", at),
		sync("home/argocd/shop/1", "home/argocd/shop", "succeeded", at),
		sync("home/argocd/orders/1", "home/argocd/orders", "succeeded", at),
		sync("a", "home/argocd/cert-manager", "succeeded", at),
		sync("b", "home/argocd/vaultwarden", "succeeded", at),
		sync("c", "home/argocd/petri", "succeeded", at),
		sync("d", "home/argocd/redgreen", "succeeded", ""),
		sync("e", "", "succeeded", at),
		sync("f", "home/argocd/twice", "succeeded", at),
		sync("g", "home/argocd/gone", "succeeded", at),
	}, apps, workloads, places, "gitlab.com")
	type row struct {
		key, repo, folder, service, env string
		elsewhere                       bool
	}
	var rows []row
	for _, r := range got {
		rows = append(rows, row{r.Key, r.RepoKey, r.Folder, r.Service, r.Environment, r.Elsewhere})
	}
	want := []row{
		{"home/argocd/redgreen/3", "github/dsswift/redgreen", "", "github/dsswift/redgreen", "production", true},
		{"home/argocd/portal/9", "github/acme/platform", "workloads/portal", "github/acme/platform/workloads/portal", "production", false},
		{"home/argocd/shop/1", "github/acme/shop", "", "github/acme/shop", "production", true},
		{"home/argocd/orders/1", "github/acme/orders", "deploy", "github/acme/orders/deploy", "production", false},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("records = %+v\nwant %+v", rows, want)
	}
	if want := (Skipped{"no_app": 1, "unplaced": 1, "no_repo_url": 1, "repo_not_tracked": 1, "unfinished": 1, "ambiguous": 1, "no_workload": 1}); !reflect.DeepEqual(m.Skipped, want) {
		t.Errorf("skipped = %v, want %v", m.Skipped, want)
	}
	if want := (Placed{"declared": 1, "workload": 2, "folder": 1}); !reflect.DeepEqual(m.Placed, want) {
		t.Errorf("placed = %v, want %v", m.Placed, want)
	}
}

func TestALinkByKeyBeatsOneByNameAndBothBeatASource(t *testing.T) {
	app := func(key, repo, path string) HubApp {
		var a HubApp
		a.Key, a.Fields.RepoURL, a.Fields.Path = key, repo, path
		return a
	}
	sync := func(app string) HubDeployment {
		var d HubDeployment
		d.Key, d.Fields.Status, d.Fields.Revision, d.Fields.DeployedAt = app+"/1", "succeeded", "abc", "2026-10-30T10:00:05Z"
		d.Relations.App = []related{{Key: app}}
		return d
	}
	to := func(from, service string) Declared {
		d := Declared{From: from, RepoKey: service, Target: Target{Key: service}}
		if ref, ok := ParseRepoURL(from, ""); ok {
			d.Source = ref
		} else {
			d.App = from
		}
		return d
	}
	apps := []HubApp{
		app("prod/argocd/jobs", "https://github.com/acme/gitops", "apps/jobs"),
		app("test/argocd/jobs", "https://github.com/acme/gitops", "apps/jobs"),
		app("prod/argocd/web", "https://github.com/acme/gitops", "apps/web"),
		app("prod/argocd/api", "https://github.com/acme/gitops", "apps/api"),
	}
	places := Places{
		Linked: []Declared{
			to("jobs", "github/acme/jobs"),
			to("prod/argocd/jobs", "github/acme/jobs-prod"),
			to("https://github.com/acme/gitops/tree/main/apps", "github/acme/apps"),
			to("https://github.com/acme/gitops/tree/main/apps/api", "github/acme/api"),
			to("https://github.com/acme/gitops/tree/release/apps/api", "github/acme/api-next"),
			to("prod/argocd/gone", "github/acme/gone"),
		},
		Declared: []Declared{{Source: RepoRef{Provider: "github", FullName: "acme/gitops", Path: "apps/web"}, RepoKey: "github/acme/web", Target: Target{Key: "github/acme/web"}}},
		Targets:  func(string) []Target { return nil },
	}
	got, m := MapSyncs([]HubDeployment{sync("prod/argocd/jobs"), sync("test/argocd/jobs"), sync("prod/argocd/web"), sync("prod/argocd/api")}, apps, nil, places, "")
	services := map[string]string{}
	for _, r := range got {
		services[r.Key] = r.Service
	}
	want := map[string]string{"prod/argocd/jobs/1": "github/acme/jobs-prod", "test/argocd/jobs/1": "github/acme/jobs", "prod/argocd/web/1": "github/acme/apps"}
	if !reflect.DeepEqual(services, want) {
		t.Errorf("services = %v, want %v", services, want)
	}
	if m.Skipped["ambiguous"] != 1 {
		t.Errorf("two links of one source to two services placed it: skipped %v", m.Skipped)
	}
	if want := []Conflict{{App: "prod/argocd/web", Linked: "github/acme/apps", Declared: "github/acme/web"}}; !reflect.DeepEqual(m.Conflicts, want) {
		t.Errorf("conflicts = %v, want %v", m.Conflicts, want)
	}
	if want := []string{"prod/argocd/gone"}; !reflect.DeepEqual(m.Unmatched, want) {
		t.Errorf("unmatched = %v, want %v", m.Unmatched, want)
	}
}

func TestAttributeByNamePrefersTheLongerComponent(t *testing.T) {
	repo := "gitlab/acme/erp"
	targets := []Target{{Key: repo + "/ad", Path: "ad", Components: []string{"activedirectory"}}, {Key: repo + "/adapi", Path: "adapi", Components: []string{"activedirectory-api"}},
		{Key: repo + "/web", Path: "web", Components: []string{"web--beta"}}, {Key: repo + "/webapp", Path: "webapp", Components: []string{"webapp"}}}
	r := func(key, env, job string) Record {
		return Record{Key: repo + "/" + key, Source: "forge", RepoKey: repo, Environment: env, Job: job, Status: "succeeded", Revision: key, FinishedAt: t0}
	}
	got, placed := Attribute([]Record{
		r("1", "production", "deploy/deploy-activedirectory-api"),
		r("2", "production", "deploy/activedirectory:deploy"),
		r("3", "production", "deploy/web--beta"),
		r("4", "webapp", ""),
		r("5", "production", "deploy/webapplication"),
	}, targets, func(Range) []string { return nil })
	services := map[string]string{}
	for _, x := range got {
		services[x.Key] = x.Service
	}
	want := map[string]string{repo + "/1": repo + "/adapi", repo + "/2": repo + "/ad", repo + "/3": repo + "/web", repo + "/4": repo + "/webapp", repo + "/5": ""}
	if !reflect.DeepEqual(services, want) {
		t.Errorf("services = %v\nwant %v", services, want)
	}
	if placed["named"] != 4 || placed["unplaced"] != 1 {
		t.Errorf("placed = %v", placed)
	}
}

func TestAttributeLeavesARootServiceItsOwnFilesOnly(t *testing.T) {
	repo := "github/acme/site"
	targets := []Target{{Key: repo, Path: ""}, {Key: repo + "/api", Path: "api"}}
	r := Record{Key: repo + "/1", Source: "forge", RepoKey: repo, Environment: "production", Status: "succeeded", Revision: "r1", FinishedAt: t0}
	got, _ := Attribute([]Record{r}, targets, func(Range) []string { return []string{"api/main.go", "api/v2/routes.go"} })
	if len(got) != 1 || got[0].Service != repo+"/api" || got[0].Folder != "api" {
		t.Errorf("files only in api placed %+v; the deepest folder holds a file", got)
	}
	got, _ = Attribute([]Record{r}, targets, func(Range) []string { return []string{"README.md", "API/main.go"} })
	if len(got) != 2 || got[0].Service != repo || got[1].Service != repo+"/api" || got[1].Key != repo+"/1@api" || got[1].From != repo+"/1" {
		t.Errorf("files in both placed %+v", got)
	}
}
