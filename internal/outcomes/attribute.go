package outcomes

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dsswift/redgreen/internal/provider"
)

// Target is a service a deployment of its repository may roll out.
type Target struct {
	Key string
	// Path is its folder from the repository root, "" for the root.
	Path string
	// Components are the names its deployments' environments and jobs call
	// it by, from its orrery.yaml.
	Components []string
}

// PathsOf answers the files a range of the repository changed, nil while
// they are not known.
type PathsOf func(Range) []string

// Placed counts deployments by what placed them on their services. For a
// repository's own (Attribute): "only" (the repository holds one), "named"
// (the environment or the job names a component), "paths" (the changed
// files sit in the services' folders), "declared" (placed before, by what
// the service says), and "unplaced". For Argo CD syncs, Mapping says.
type Placed map[string]int

// Attribute places each deployment of one repository on the service it
// rolled out, from the strongest signal the forge gives:
//
//  1. The repository holds one service: that one.
//  2. The environment or the job names one or more services' components.
//  3. The files changed since the environment last took another revision
//     sit in one or more services' folders. A forge that runs one job per
//     service for a revision says nothing of which job is whose, so the
//     revision's deployments to one environment are taken together: the
//     first of them carries every service its changes touched that no named
//     job already claims, failed if any of them failed, ended when the last
//     ended; the rest stay unplaced.
//
// A deployment that rolled out several services is one record per service:
// DORA counts each service's own deliveries, and each service's stream gets
// its own previous deployment, failures, and restores. The first service by
// key keeps the forge deployment's key; each other is keyed
// `<key>@<service folder>`. What no signal places stays unplaced, as before.
func Attribute(records []Record, targets []Target, paths PathsOf) ([]Record, Placed) {
	placed := Placed{}
	var out, open []Record
	claimed := map[string]map[string]bool{}
	for _, r := range records {
		switch {
		case r.Service != "":
			placed["declared"]++
			out = append(out, r)
		case len(targets) == 0:
			placed["unplaced"]++
			out = append(out, r)
		case len(targets) == 1:
			placed["only"]++
			r.Service = targets[0].Key
			out = append(out, r)
		default:
			named := byName(targets, r.Environment, r.Job)
			if len(named) == 0 {
				open = append(open, r)
				continue
			}
			placed["named"]++
			g := group(r)
			if claimed[g] == nil {
				claimed[g] = map[string]bool{}
			}
			for _, t := range named {
				claimed[g][t.Key] = true
			}
			out = append(out, split(r, named)...)
		}
	}
	groups := map[string][]Record{}
	var order []string
	for _, r := range open {
		g := group(r)
		if groups[g] == nil {
			order = append(order, g)
		}
		groups[g] = append(groups[g], r)
	}
	// The newest first, so a bounded number of reads per pass reaches the
	// recent deployments before the old ones.
	latest := func(g string) time.Time {
		var at time.Time
		for _, r := range groups[g] {
			if r.FinishedAt.After(at) {
				at = r.FinishedAt
			}
		}
		return at
	}
	sort.SliceStable(order, func(i, j int) bool { return latest(order[i]).After(latest(order[j])) })
	before := succeededByEnvironment(records)
	for _, g := range order {
		list := groups[g]
		sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
		changed := paths(Range{Base: lastOther(before[list[0].Environment], list), Head: list[0].Revision})
		var touched []Target
		if changed != nil {
			for _, t := range owners(changed, targets) {
				if !claimed[g][t.Key] {
					touched = append(touched, t)
				}
			}
		}
		if len(touched) == 0 {
			placed["unplaced"] += len(list)
			out = append(out, list...)
			continue
		}
		first := list[0]
		for _, r := range list[1:] {
			if r.Status == provider.DeployFailed {
				first.Status = provider.DeployFailed
			}
			if r.FinishedAt.After(first.FinishedAt) {
				first.FinishedAt = r.FinishedAt
			}
		}
		placed["paths"]++
		placed["unplaced"] += len(list) - 1
		out = append(out, split(first, touched)...)
		out = append(out, list[1:]...)
	}
	return out, placed
}

// group is a revision rolled out to one environment.
func group(r Record) string { return r.Environment + "\n" + r.Revision }

// split is one record per target, the first under the deployment's own key.
func split(r Record, targets []Target) []Record {
	sort.Slice(targets, func(i, j int) bool { return targets[i].Key < targets[j].Key })
	out := make([]Record, 0, len(targets))
	for i, t := range targets {
		x := r
		x.Service, x.Folder = t.Key, t.Path
		if i > 0 {
			x.From = r.Key
			x.Key = r.Key + "@" + strings.TrimPrefix(t.Key, r.RepoKey+"/")
		}
		out = append(out, x)
	}
	return out
}

// byName is the targets whose component a name holds as a whole word. A
// component held inside a longer one that also matched is the longer one's:
// "api" in "deploy-orders-api" is the orders-api component's.
func byName(targets []Target, names ...string) []Target {
	type hit struct {
		target    Target
		component string
	}
	var hits []hit
	for _, t := range targets {
		for _, c := range t.Components {
			c = strings.ToLower(strings.TrimSpace(c))
			if c != "" && namedIn(c, names) {
				hits = append(hits, hit{t, c})
				break
			}
		}
	}
	var out []Target
	for _, h := range hits {
		inner := false
		for _, o := range hits {
			if o.component != h.component && strings.Contains(o.component, h.component) {
				inner = true
				break
			}
		}
		if !inner {
			out = append(out, h.target)
		}
	}
	return out
}

// namedIn says whether any name holds c with no letter or digit either side.
func namedIn(c string, names []string) bool {
	for _, n := range names {
		n = strings.ToLower(n)
		for from := 0; from < len(n); {
			i := strings.Index(n[from:], c)
			if i < 0 {
				break
			}
			i += from
			end := i + len(c)
			if (i == 0 || !wordy(n[i-1])) && (end == len(n) || !wordy(n[end])) {
				return true
			}
			from = i + 1
		}
	}
	return false
}

func wordy(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }

// owners is the targets holding the changed files, each file held by the
// deepest folder it sits in. A file in no target's folder counts for none.
func owners(changed []string, targets []Target) []Target {
	held := map[string]Target{}
	for _, file := range changed {
		file = strings.ToLower(strings.Trim(file, "/"))
		best, depth := Target{}, -1
		for _, t := range targets {
			p := strings.ToLower(strings.Trim(t.Path, "/"))
			if (p == "" || file == p || strings.HasPrefix(file, p+"/")) && len(p) > depth {
				best, depth = t, len(p)
			}
		}
		if depth >= 0 {
			held[best.Key] = best
		}
	}
	out := make([]Target, 0, len(held))
	for _, t := range held {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// succeededByEnvironment is the repository's successful forge deployments
// per environment, oldest first.
func succeededByEnvironment(records []Record) map[string][]Record {
	out := map[string][]Record{}
	for _, r := range records {
		if r.Source == "forge" && r.Status == provider.DeploySucceeded {
			out[r.Environment] = append(out[r.Environment], r)
		}
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return list[i].FinishedAt.Before(list[j].FinishedAt) })
	}
	return out
}

// lastOther is the revision the environment last took before the group
// began that is not the group's own, "" when it took none.
func lastOther(succeeded []Record, group []Record) string {
	began := group[0].FinishedAt
	for _, r := range group[1:] {
		if r.FinishedAt.Before(began) {
			began = r.FinishedAt
		}
	}
	i := sort.Search(len(succeeded), func(i int) bool { return !succeeded[i].FinishedAt.Before(began) })
	for i--; i >= 0; i-- {
		if succeeded[i].Revision != group[0].Revision {
			return succeeded[i].Revision
		}
	}
	return ""
}

// Places is what places an Argo CD app's syncs in a repository and on one
// of its services.
type Places struct {
	// Linked are the links the operator who runs redgreen declares in its
	// own settings (ARGOCD_SERVICES): an app by its key or name, or a source.
	Linked []Declared
	// Declared are the Argo CD sources services name in their orrery.yaml.
	Declared []Declared
	// Targets are a tracked repository's services, by its key; nil for a
	// repository redgreen does not watch.
	Targets func(repoKey string) []Target
}

// Declared is one app or source a service is said to be deployed by.
type Declared struct {
	// From is the words that said so, as written.
	From string
	// App is an Argo CD app's key on the hub, or its name, the key's last
	// part, which names every app so called; "" when Source says it.
	App     string
	Source  RepoRef
	RepoKey string
	Target  Target
}

// Mapping is how the syncs were placed, and why the rest were left out.
type Mapping struct {
	// Placed counts the syncs by what placed them: "linked", "declared",
	// "workload", or "folder".
	Placed  Placed
	Skipped Skipped
	// Conflicts are the apps a link places on another service than the one
	// whose orrery.yaml names its source. The link wins.
	Conflicts []Conflict
	// Unmatched are the links, as written, that name no app the hub holds.
	Unmatched []string
}

// Conflict is an app a link and a service's orrery.yaml place apart.
type Conflict struct {
	App, Linked, Declared string
}

// MapSyncs turns the hub's Argo CD syncs into records of the repositories
// redgreen watches, each placed from the strongest signal there is:
//
//  1. Someone says which service the app deploys. The operator's link names
//     the app, by key, then by name, or the source its manifests are read
//     from; a service's orrery.yaml names that source. The longest source
//     named wins; two links or two services naming the same one place
//     neither. Where a link and an orrery.yaml disagree, the link wins: the
//     operator runs this estate, and a service's file is written once for
//     every estate that runs it.
//  2. The app's workload names its repository and folder: the service in
//     that folder, or the repository's only one.
//  3. The app's source sits in a watched repository, inside a service's
//     own folder: that service. A repository's root holds every folder, so
//     it is no evidence, and a root service is placed only by naming it.
//
// A sync's revision is the manifests' commit; where they are kept in another
// repository than the service's, its release tags stand in (`Elsewhere`).
func MapSyncs(syncs []HubDeployment, apps []HubApp, workloads []HubWorkload, places Places, gitlabHost string) ([]Record, Mapping) {
	appBy := map[string]HubApp{}
	sources := map[string]RepoRef{}
	for _, a := range apps {
		appBy[a.Key] = a
		if source, ok := appSource(a, gitlabHost); ok {
			sources[a.Key] = source
		}
	}
	workloadBy := map[string]HubWorkload{}
	for _, w := range workloads {
		workloadBy[w.Key] = w
	}
	m := Mapping{Placed: Placed{}, Skipped: Skipped{}, Unmatched: unmatched(places.Linked, apps, sources)}
	conflicts := map[string]Conflict{}
	var out []Record
	for _, d := range syncs {
		at, err := time.Parse(time.RFC3339, d.Fields.DeployedAt)
		if (d.Fields.Status != "succeeded" && d.Fields.Status != "failed") || err != nil || d.Fields.Revision == "" {
			m.Skipped["unfinished"]++
			continue
		}
		if len(d.Relations.App) == 0 {
			m.Skipped["no_app"]++
			continue
		}
		app, ok := appBy[d.Relations.App[0].Key]
		if !ok {
			m.Skipped["no_app"]++
			continue
		}
		env := "production"
		if len(app.Relations.Environment) > 0 {
			env = app.Relations.Environment[0].Key
		}
		source, sourced := sources[app.Key]
		r := Record{Key: d.Key, Source: "argo", Environment: env, Status: d.Fields.Status, Revision: d.Fields.Revision, FinishedAt: at}
		link, linkAmbiguous := linkedFor(app.Key, source, sourced, places.Linked)
		var own *Declared
		ownAmbiguous := false
		if sourced {
			own, ownAmbiguous = declaredFor(source, places.Declared)
		}
		said, how := link, "linked"
		switch {
		case link != nil:
			if own != nil && own.Target.Key != link.Target.Key {
				conflicts[app.Key] = Conflict{App: app.Key, Linked: link.Target.Key, Declared: own.Target.Key}
			}
		case linkAmbiguous || ownAmbiguous:
			m.Skipped["ambiguous"]++
			continue
		default:
			said, how = own, "declared"
		}
		if said != nil {
			r.RepoKey, r.Folder, r.Service = said.RepoKey, said.Target.Path, said.Target.Key
			r.Elsewhere = !sourced || !strings.EqualFold(source.Key(), said.RepoKey)
			m.Placed[how]++
			out = append(out, r)
			continue
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
				m.Placed["workload"]++
				out = append(out, r)
				continue
			}
		}
		if sourced {
			if t, ok := folderHolding(places.Targets(source.Key()), source.Path); ok {
				r.RepoKey, r.Folder, r.Service = source.Key(), t.Path, t.Key
				m.Placed["folder"]++
				out = append(out, r)
				continue
			}
		}
		m.Skipped[reason]++
	}
	for _, c := range conflicts {
		m.Conflicts = append(m.Conflicts, c)
	}
	sort.Slice(m.Conflicts, func(i, j int) bool { return m.Conflicts[i].App < m.Conflicts[j].App })
	return out, m
}

// appSource is the repository and folder an app reads its manifests from.
func appSource(a HubApp, gitlabHost string) (RepoRef, bool) {
	source, ok := ParseRepoURL(a.Fields.RepoURL, gitlabHost)
	if ok && a.Fields.Path != "" {
		source.Path = strings.TrimPrefix(path.Clean("/"+source.Path+"/"+a.Fields.Path), "/")
	}
	return source, ok
}

// appNamed says whether a link names the app with this key: by the key
// itself, or by its name, the key's last part.
func appNamed(link, key string) bool {
	return strings.EqualFold(link, key) || strings.EqualFold(link, key[strings.LastIndex(key, "/")+1:])
}

// linkedFor is the operator's link for an app: the one naming its key, else
// its name, else the longest source holding the app's; ambiguous when two
// links of that one source name different services.
func linkedFor(key string, source RepoRef, sourced bool, links []Declared) (*Declared, bool) {
	var byName *Declared
	var bySource []Declared
	for i, l := range links {
		switch {
		case l.App == "":
			bySource = append(bySource, l)
		case strings.EqualFold(l.App, key):
			return &links[i], false
		case appNamed(l.App, key):
			byName = &links[i]
		}
	}
	if byName != nil || !sourced {
		return byName, false
	}
	return declaredFor(source, bySource)
}

// unmatched is the links, as written, that name no app the hub holds.
func unmatched(links []Declared, apps []HubApp, sources map[string]RepoRef) []string {
	var out []string
	for _, l := range links {
		found := false
		for _, a := range apps {
			source, sourced := sources[a.Key]
			if l.App != "" && appNamed(l.App, a.Key) || l.App == "" && sourced && SameRepo(l.Source, source) && under(source.Path, l.Source.Path) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, l.From)
		}
	}
	sort.Strings(out)
	return out
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

// PipelineDeployments are a pipeline's deployments for a service that deploys
// by its pipeline, from its kept runs: per revision, the first run that
// succeeded on the default branch, or for a tag or a release. A scheduled
// run, a pull request's, or another branch's rolled out no change, and a
// failed run is a broken build more often than a broken environment, so
// neither counts.
func PipelineDeployments(runs []provider.Run, defaultBranch, environment, job string) []provider.Deployment {
	first := map[string]provider.Run{}
	for _, r := range runs {
		if r.Status != provider.StatusCompleted || r.Conclusion != provider.Success || r.SHA == "" {
			continue
		}
		switch r.Trigger {
		case provider.TriggerTag, provider.TriggerRelease:
		case provider.TriggerPush, provider.TriggerManual:
			if r.Ref != defaultBranch {
				continue
			}
		default:
			continue
		}
		if had, ok := first[r.SHA]; !ok || r.UpdatedAt.Before(had.UpdatedAt) {
			first[r.SHA] = r
		}
	}
	out := make([]provider.Deployment, 0, len(first))
	for _, r := range first {
		finished := r.UpdatedAt
		out = append(out, provider.Deployment{ID: "run-" + r.ID, Environment: environment, Status: provider.DeploySucceeded, Ref: r.Ref, SHA: r.SHA,
			CreatedAt: r.CreatedAt, FinishedAt: &finished, URL: r.URL, Actor: r.Actor, Job: job})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FinishedAt.Before(*out[j].FinishedAt) })
	return out
}
