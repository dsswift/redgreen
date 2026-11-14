package outcomes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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

// HubApp is an Argo CD application.
type HubApp struct {
	Key    string `json:"key"`
	Fields struct {
		RepoURL string `json:"repo_url"`
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

// MapSyncs turns the hub's Argo CD syncs into records of the repositories
// redgreen watches. A sync names an app; the app names its workload; the
// workload's repo URL gives the repository and the folder.
func MapSyncs(syncs []HubDeployment, apps []HubApp, workloads []HubWorkload, tracked func(RepoRef) bool, gitlabHost string) ([]Record, Skipped) {
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
		var w HubWorkload
		if len(app.Relations.Workload) > 0 {
			w, ok = workloadBy[app.Relations.Workload[0].Key]
		}
		if len(app.Relations.Workload) == 0 || !ok {
			skipped["no_workload"]++
			continue
		}
		ref, ok := ParseRepoURL(w.Fields.Repo, gitlabHost)
		if !ok {
			skipped["no_repo_url"]++
			continue
		}
		if !tracked(ref) {
			skipped["repo_not_tracked"]++
			continue
		}
		env := "production"
		if len(app.Relations.Environment) > 0 {
			env = app.Relations.Environment[0].Key
		}
		source, _ := ParseRepoURL(app.Fields.RepoURL, gitlabHost)
		out = append(out, Record{Key: d.Key, Source: "argo", RepoKey: ref.Key(), Folder: ref.Path, Environment: env, Status: d.Fields.Status,
			Revision: d.Fields.Revision, FinishedAt: at, Elsewhere: !SameRepo(source, ref)})
	}
	return out, skipped
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

// Read is one pass over what the hub holds: the workloads that place a sync
// or an incident in a repository, the Argo CD apps, the syncs of an app
// since `from`, and the incidents on a workload since. A sync
// reaches a repository only through an app and its workload, and an incident
// only through its workload, so a hub with no workloads is asked nothing more.
func (r Reader) Read(ctx context.Context, from time.Time) ([]HubDeployment, []HubApp, []HubWorkload, []HubIncident, error) {
	workloads, err := Entities[HubWorkload](ctx, r, Ask{Blueprint: "sw_workload", Fields: []string{"repo"}, Relations: []string{}})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("workloads: %w", err)
	}
	if len(workloads) == 0 {
		return nil, nil, nil, nil, nil
	}
	apps, err := Entities[HubApp](ctx, r, Ask{Blueprint: "argocd_app", Fields: []string{"repo_url"}, Relations: []string{"workload", "environment"}})
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
	incidents, err := Entities[HubIncident](ctx, r, Ask{Blueprint: "it_incident", Filter: map[string]any{"and": []any{since("opened_at", from), map[string]any{"relation": "workload"}}}, Fields: []string{"severity", "opened_at", "mitigated_at", "resolved_at"}, Relations: []string{"workload"}})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("incidents: %w", err)
	}
	incidents = after(incidents, func(i HubIncident) string { return i.Fields.OpenedAt }, from)
	return syncs, apps, workloads, incidents, nil
}
