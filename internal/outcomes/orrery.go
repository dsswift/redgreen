package outcomes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/state"
)

// The hub's entities as /v1/entities answers them, the parts read here.

type related struct {
	Key string `json:"key"`
}

// HubDeployment is an sw_deployment another source wrote, such as an Argo CD sync.
type HubDeployment struct {
	Key    string `json:"key"`
	Fields struct {
		Status     string `json:"status"`
		Revision   string `json:"revision"`
		DeployedAt string `json:"deployed_at"`
	} `json:"fields"`
	Relations struct {
		App []related `json:"app"`
	} `json:"relations"`
}

// HubReport is an sw_deployment that names the forge job it records by its
// address: one a CI job reports itself, or redgreen's own.
type HubReport struct {
	Key    string `json:"key"`
	Owner  string `json:"owner"`
	Fields struct {
		URL        string `json:"url"`
		DeployedAt string `json:"deployed_at"`
	} `json:"fields"`
}

// Handover is what the hub says of the forge jobs another source reports.
type Handover struct {
	// Elsewhere is, by the forge job's address, the entity another source keeps for it.
	Elsewhere map[string]string
	// Copies are redgreen's own entities for those jobs, to be deleted.
	Copies []string
}

// HandOver reads the reports for the jobs another source keeps. redgreen is
// whoever owns one of its own keys (`own`), so it needs no name of its own; a
// job reported by redgreen alone is redgreen's. Until the hub holds one of its
// keys, nothing is handed over.
func HandOver(reports []HubReport, own func(key string) bool) Handover {
	self := map[string]bool{}
	for _, r := range reports {
		if own(r.Key) {
			self[r.Owner] = true
		}
	}
	out := Handover{Elsewhere: map[string]string{}}
	if len(self) == 0 {
		return out
	}
	byURL := map[string][]HubReport{}
	for _, r := range reports {
		if r.Fields.URL != "" {
			byURL[r.Fields.URL] = append(byURL[r.Fields.URL], r)
		}
	}
	for url, list := range byURL {
		other := ""
		for _, r := range list {
			if !self[r.Owner] && (other == "" || r.Key < other) {
				other = r.Key
			}
		}
		if other == "" {
			continue
		}
		out.Elsewhere[url] = other
		for _, r := range list {
			if self[r.Owner] {
				out.Copies = append(out.Copies, r.Key)
			}
		}
	}
	sort.Strings(out.Copies)
	return out
}

// HubApp is an Argo CD application: where its manifests are read, and what
// it deploys.
type HubApp struct {
	Key    string `json:"key"`
	Fields struct {
		RepoURL string `json:"repo_url"`
		Path    string `json:"path"`
	} `json:"fields"`
	Relations struct {
		Workload    []related `json:"workload"`
		Environment []related `json:"environment"`
	} `json:"relations"`
}

// HubWorkload is a catalog workload; its repo URL names the repository and folder it is built from.
type HubWorkload struct {
	Key    string `json:"key"`
	Fields struct {
		Repo string `json:"repo"`
	} `json:"fields"`
}

// HubIncident is an it_incident Dispatch wrote.
type HubIncident struct {
	Key    string `json:"key"`
	Fields struct {
		Severity    string `json:"severity"`
		OpenedAt    string `json:"opened_at"`
		MitigatedAt string `json:"mitigated_at"`
		ResolvedAt  string `json:"resolved_at"`
	} `json:"fields"`
	Relations struct {
		Workload []related `json:"workload"`
	} `json:"relations"`
}

// Skipped counts why syncs or incidents were left out.
type Skipped map[string]int

// Places is what places an Argo CD app's syncs in a repository and on one
// of its services.
type Places struct {
	// Declared are the Argo CD sources services name in their orrery.yaml.
	Declared []Declared
	// Targets are a tracked repository's services, by its key; nil for a
	// repository redgreen does not watch.
	Targets func(repoKey string) []Target
}

// Declared is one source a service says its Argo CD apps deploy it from.
type Declared struct {
	Source  RepoRef
	RepoKey string
	Target  Target
}

// MapSyncs turns the hub's Argo CD syncs into records of the repositories
// redgreen watches, each placed from the strongest signal there is:
//
//  1. A service's orrery.yaml names the app's source, the repository and
//     the folder its manifests are read from: that service. The longest
//     source named wins; two services naming the same one place neither.
//  2. The app's workload names its repository and folder: the service in
//     that folder, or the repository's only one.
//  3. The app's source sits in a watched repository, inside a service's
//     own folder: that service. A repository's root holds every folder, so
//     it is no evidence, and a root service is placed only by naming it.
//
// A sync's revision is the manifests' commit; where they are kept in another
// repository than the service's, its release tags stand in (`Elsewhere`).
func MapSyncs(syncs []HubDeployment, apps []HubApp, workloads []HubWorkload, places Places, gitlabHost string) ([]Record, Skipped) {
	appBy := map[string]HubApp{}
	for _, a := range apps {
		appBy[a.Key] = a
	}
	workloadBy := map[string]HubWorkload{}
	for _, w := range workloads {
		workloadBy[w.Key] = w
	}
	skipped := Skipped{}
	var out []Record
	for _, d := range syncs {
		at, err := time.Parse(time.RFC3339, d.Fields.DeployedAt)
		if (d.Fields.Status != "succeeded" && d.Fields.Status != "failed") || err != nil || d.Fields.Revision == "" {
			skipped["unfinished"]++
			continue
		}
		if len(d.Relations.App) == 0 {
			skipped["no_app"]++
			continue
		}
		app, ok := appBy[d.Relations.App[0].Key]
		if !ok {
			skipped["no_app"]++
			continue
		}
		env := "production"
		if len(app.Relations.Environment) > 0 {
			env = app.Relations.Environment[0].Key
		}
		source, sourced := ParseRepoURL(app.Fields.RepoURL, gitlabHost)
		if sourced && app.Fields.Path != "" {
			source.Path = strings.TrimPrefix(path.Clean("/"+source.Path+"/"+app.Fields.Path), "/")
		}
		r := Record{Key: d.Key, Source: "argo", Environment: env, Status: d.Fields.Status, Revision: d.Fields.Revision, FinishedAt: at}
		if sourced {
			if decl, ambiguous := declaredFor(source, places.Declared); ambiguous {
				skipped["ambiguous"]++
				continue
			} else if decl != nil {
				r.RepoKey, r.Folder, r.Service = decl.RepoKey, decl.Target.Path, decl.Target.Key
				r.Elsewhere = !strings.EqualFold(source.Key(), decl.RepoKey)
				out = append(out, r)
				continue
			}
		}
		reason := "unplaced"
		if len(app.Relations.Workload) > 0 {
			w, ok := workloadBy[app.Relations.Workload[0].Key]
			ref, parsed := ParseRepoURL(w.Fields.Repo, gitlabHost)
			targets := places.Targets(ref.Key())
			switch {
			case !ok:
				reason = "no_workload"
			case !parsed:
				reason = "no_repo_url"
			case targets == nil:
				reason = "repo_not_tracked"
			default:
				r.RepoKey, r.Folder, r.Service = ref.Key(), ref.Path, serviceAt(targets, ref.Path)
				r.Elsewhere = !SameRepo(source, ref)
				out = append(out, r)
				continue
			}
		}
		if sourced {
			if t, ok := folderHolding(places.Targets(source.Key()), source.Path); ok {
				r.RepoKey, r.Folder, r.Service = source.Key(), t.Path, t.Key
				out = append(out, r)
				continue
			}
		}
		skipped[reason]++
	}
	return out, skipped
}

// under says whether folder sits at or below parent, however cased.
func under(folder, parent string) bool {
	folder, parent = strings.ToLower(strings.Trim(folder, "/")), strings.ToLower(strings.Trim(parent, "/"))
	return parent == "" || folder == parent || strings.HasPrefix(folder, parent+"/")
}

// declaredFor is the service whose named source holds an app's, the longest
// such source; ambiguous when two services name that one.
func declaredFor(source RepoRef, declared []Declared) (best *Declared, ambiguous bool) {
	depth := -1
	for i, d := range declared {
		if !SameRepo(d.Source, source) || !under(source.Path, d.Source.Path) {
			continue
		}
		n := len(strings.Trim(d.Source.Path, "/"))
		switch {
		case n > depth:
			best, depth, ambiguous = &declared[i], n, false
		case n == depth && best.Target.Key != d.Target.Key:
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, true
	}
	return best, false
}

// serviceAt is the service in folder, or the repository's only one when the
// folder is its root.
func serviceAt(targets []Target, folder string) string {
	for _, t := range targets {
		if strings.EqualFold(strings.Trim(t.Path, "/"), strings.Trim(folder, "/")) {
			return t.Key
		}
	}
	if strings.Trim(folder, "/") == "" && len(targets) == 1 {
		return targets[0].Key
	}
	return ""
}

// folderHolding is the deepest service folder, never the root, that holds folder.
func folderHolding(targets []Target, folder string) (Target, bool) {
	var best Target
	depth := 0
	for _, t := range targets {
		p := strings.Trim(t.Path, "/")
		if p != "" && under(folder, p) && len(p) > depth {
			best, depth = t, len(p)
		}
	}
	return best, depth > 0
}

// MapIncidents ties each incident to the repository and folder of its workload.
func MapIncidents(list []HubIncident, workloads []HubWorkload, gitlabHost string) ([]Incident, Skipped) {
	workloadBy := map[string]HubWorkload{}
	for _, w := range workloads {
		workloadBy[w.Key] = w
	}
	parse := func(s string) *time.Time {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil
		}
		return &t
	}
	skipped := Skipped{}
	var out []Incident
	for _, i := range list {
		opened := parse(i.Fields.OpenedAt)
		if opened == nil {
			skipped["unopened"]++
			continue
		}
		if len(i.Relations.Workload) == 0 {
			skipped["no_workload"]++
			continue
		}
		w, ok := workloadBy[i.Relations.Workload[0].Key]
		if !ok {
			skipped["no_workload"]++
			continue
		}
		ref, ok := ParseRepoURL(w.Fields.Repo, gitlabHost)
		if !ok {
			skipped["no_repo_url"]++
			continue
		}
		out = append(out, Incident{RepoKey: ref.Key(), Folder: ref.Path, Severity: i.Fields.Severity, OpenedAt: *opened, MitigatedAt: parse(i.Fields.MitigatedAt), ResolvedAt: parse(i.Fields.ResolvedAt)})
	}
	return out, skipped
}

// Reader reads the hub with an access token: a source token may not read.
// Every page goes through `provider.Get`: the hub tags each list answer, so a
// page asked again with its tag is answered 304 with nothing when nothing the
// list reads has moved, and the page kept from the last answer is used.
type Reader struct {
	URL   string
	Fetch *provider.Fetcher
}

// NewReader reads the hub at url with token, keeping each page's tag and
// what it held in store. With no store, nothing is kept and every page is
// asked in full.
func NewReader(url, token string, store *state.Store, client *http.Client, log *slog.Logger) Reader {
	auth := func(context.Context) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer " + token}, nil
	}
	return Reader{URL: strings.TrimRight(url, "/"), Fetch: &provider.Fetcher{Store: store, HTTP: client, Scope: "orrery-read", Auth: auth, Log: log}}
}

// page is how many entities one list call asks for.
const page = 2000

// Ask is one list read: the blueprint, which of its entities, and only the
// fields and relations a mapping here reads. The hub answers each entity in
// its usual shape, cut to those.
type Ask struct {
	Blueprint string
	Filter    any
	Fields    []string
	Relations []string
}

// listed is one page of a list answer, decoded into T.
type listed[T any] struct {
	Entities []T `json:"entities"`
	Total    int `json:"total"`
}

// Entities reads every entity an ask matches, decoded into T.
func Entities[T any](ctx context.Context, r Reader, ask Ask) ([]T, error) {
	var out []T
	for offset := 0; ; offset += page {
		q := url.Values{
			"blueprint": {ask.Blueprint},
			"limit":     {fmt.Sprint(page)},
			"offset":    {fmt.Sprint(offset)},
			"fields":    {strings.Join(ask.Fields, ",")},
			"relations": {strings.Join(ask.Relations, ",")},
		}
		if ask.Filter != nil {
			b, err := json.Marshal(ask.Filter)
			if err != nil {
				return nil, err
			}
			q.Set("filter", string(b))
		}
		address := r.URL + "/v1/entities?" + q.Encode()
		var res listed[T]
		var err error
		if r.Fetch.Store == nil {
			res, err = provider.Once(ctx, r.Fetch, address, provider.JSON[listed[T]])
		} else {
			res, err = provider.Get(ctx, r.Fetch, address, provider.JSON[listed[T]])
		}
		if err != nil {
			return nil, err
		}
		out = append(out, res.Entities...)
		if len(out) >= res.Total || len(res.Entities) == 0 {
			return out, nil
		}
	}
}

// since is a filter on a datetime field from a moment on. The hub is asked
// from the start of that day, so the question, and the tag it is answered
// with, stay the same all day; the caller trims to the moment itself.
func since(field string, from time.Time) map[string]any {
	return map[string]any{"field": field, "op": "gte", "value": from.UTC().Truncate(24 * time.Hour).Format(time.RFC3339)}
}

// after keeps what a datetime field says is at or after a moment; a value that
// is not a time is kept for the mapping to judge.
func after[T any](list []T, at func(T) string, from time.Time) []T {
	out := list[:0]
	for _, x := range list {
		t, err := time.Parse(time.RFC3339, at(x))
		if err == nil && t.Before(from) {
			continue
		}
		out = append(out, x)
	}
	return out
}

// Reported reads the deployments since `from` that name a forge job's address
// and are no Argo CD sync, whoever wrote them. A forge job another source
// reports is that source's: redgreen adds its outcome to it and keeps no
// entity of its own (`HandOver`).
func (r Reader) Reported(ctx context.Context, from time.Time) ([]HubReport, error) {
	filter := map[string]any{"and": []any{since("deployed_at", from), map[string]any{"field": "url", "op": "exists"}, map[string]any{"not": map[string]any{"relation": "app"}}}}
	list, err := Entities[HubReport](ctx, r, Ask{Blueprint: "sw_deployment", Filter: filter, Fields: []string{"url", "deployed_at"}, Relations: []string{}})
	if err != nil {
		return nil, fmt.Errorf("reported deployments: %w", err)
	}
	return after(list, func(d HubReport) string { return d.Fields.DeployedAt }, from), nil
}

// Read is one pass over what the hub holds: the workloads that place a sync
// or an incident in a repository, the Argo CD apps, the syncs of an app
// since `from`, and the incidents on a workload since. An app places its
// syncs by its source as well as by its workload, so its syncs are read on a
// hub with no workloads; an incident is placed only through its workload, so
// a hub without one is asked for none.
func (r Reader) Read(ctx context.Context, from time.Time) ([]HubDeployment, []HubApp, []HubWorkload, []HubIncident, error) {
	workloads, err := Entities[HubWorkload](ctx, r, Ask{Blueprint: "sw_workload", Fields: []string{"repo"}, Relations: []string{}})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("workloads: %w", err)
	}
	apps, err := Entities[HubApp](ctx, r, Ask{Blueprint: "argocd_app", Fields: []string{"repo_url", "path"}, Relations: []string{"workload", "environment"}})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("argo cd apps: %w", err)
	}
	var syncs []HubDeployment
	if len(apps) > 0 {
		syncs, err = Entities[HubDeployment](ctx, r, Ask{Blueprint: "sw_deployment", Filter: map[string]any{"and": []any{since("deployed_at", from), map[string]any{"relation": "app"}}}, Fields: []string{"status", "revision", "deployed_at"}, Relations: []string{"app"}})
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("deployments: %w", err)
		}
		syncs = after(syncs, func(d HubDeployment) string { return d.Fields.DeployedAt }, from)
	}
	if len(workloads) == 0 {
		return syncs, apps, workloads, nil, nil
	}
	incidents, err := Entities[HubIncident](ctx, r, Ask{Blueprint: "it_incident", Filter: map[string]any{"and": []any{since("opened_at", from), map[string]any{"relation": "workload"}}}, Fields: []string{"severity", "opened_at", "mitigated_at", "resolved_at"}, Relations: []string{"workload"}})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("incidents: %w", err)
	}
	incidents = after(incidents, func(i HubIncident) string { return i.Fields.OpenedAt }, from)
	return syncs, apps, workloads, incidents, nil
}
