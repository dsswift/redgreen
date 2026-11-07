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
	if r, ok := RangeOf(records[2], records, nil); !ok || r != (Range{Base: "a", Head: "c"}) {
		t.Errorf("range = %+v; a failed deployment is never the base", r)
	}
	if r, _ := RangeOf(records[0], records, nil); r != (Range{Head: "a"}) {
		t.Errorf("the first of a stream has no base: %+v", r)
	}
	manifest := rec("m", "succeeded", 10)
	manifest.Elsewhere = true
	releases := []provider.Release{{Tag: "v2", PublishedAt: h(9)}, {Tag: "v3-rc", PublishedAt: h(9.5), Prerelease: true}, {Tag: "v1", PublishedAt: h(-5)}, {Tag: "v9", PublishedAt: h(20)}}
	if r, ok := RangeOf(manifest, []Record{manifest}, releases); !ok || r.Head != "v2" {
		t.Errorf("a manifest revision stands in as the newest full release by then: %+v", r)
	}
	if _, ok := RangeOf(manifest, []Record{manifest}, nil); ok {
		t.Error("with no release to stand in, the range is unknown")
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
	app := func(key, repo, workload, env string) HubApp {
		var a HubApp
		a.Key, a.Fields.RepoURL = key, repo
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
		app("home/argocd/redgreen", "https://github.com/acme/platform", "redgreen", "production"),
		app("home/argocd/portal", "https://github.com/acme/platform", "portal", ""),
		app("home/argocd/cert-manager", "https://charts.jetstack.io", "", ""),
		app("home/argocd/vaultwarden", "https://github.com/acme/platform", "vaultwarden", ""),
		app("home/argocd/petri", "https://github.com/acme/platform", "petri", ""),
	}
	workloads := []HubWorkload{
		workload("redgreen", "https://github.com/dsswift/redgreen"),
		workload("portal", "https://github.com/acme/platform/tree/main/workloads/portal"),
		workload("vaultwarden", ""),
		workload("petri", "https://github.com/dsswift/petri"),
	}
	tracked := func(r RepoRef) bool {
		return r.Key() == "github/dsswift/redgreen" || r.Key() == "github/acme/platform"
	}
	at := "2026-10-30T10:00:05Z"
	got, skipped := MapSyncs([]HubDeployment{
		sync("home/argocd/redgreen/3", "home/argocd/redgreen", "succeeded", at),
		sync("home/argocd/portal/9", "home/argocd/portal", "failed", at),
		sync("a", "home/argocd/cert-manager", "succeeded", at),
		sync("b", "home/argocd/vaultwarden", "succeeded", at),
		sync("c", "home/argocd/petri", "succeeded", at),
		sync("d", "home/argocd/redgreen", "succeeded", ""),
		sync("e", "", "succeeded", at),
	}, apps, workloads, tracked, "gitlab.com")
	if len(got) != 2 || got[0].RepoKey != "github/dsswift/redgreen" || !got[0].Elsewhere || got[1].Folder != "workloads/portal" || got[1].Elsewhere || got[1].Environment != "production" {
		t.Errorf("records = %+v", got)
	}
	if want := (Skipped{"no_app": 1, "no_workload": 1, "no_repo_url": 1, "repo_not_tracked": 1, "unfinished": 1}); !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}
