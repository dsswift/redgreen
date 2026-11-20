package outcomes

import (
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

// Placed counts how a repository's deployments found their services: "only"
// (the repository holds one), "named" (the environment or the job names a
// component), "paths" (the changed files sit in the services' folders),
// "declared" (placed before, by what the service says), and "unplaced".
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
