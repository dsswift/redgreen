package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/outcomes"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/state"
)

// commitReadsPerRepo bounds the compares made for one repository in one pass,
// so a backlog never starves a sweep. The rest wait for the next pass.
const commitReadsPerRepo = 10

// window is how far back deployments are read: the 30-day windows and the
// one before them, with room for a deployment's neighbours.
const window = 200 * 24 * time.Hour

// argoScope is where the outcomes added to deployments another source owns
// (Argo CD syncs, and forge jobs a CI job reports itself) are written.
const argoScope = "argo-outcomes"

// pathReadsPerRepo bounds the changed-file reads made for one repository in
// one pass, like commitReadsPerRepo.
const pathReadsPerRepo = 10

// pushOutcomes works out every deployment's outcome and writes it: forge and
// pipeline deployments as entities of their own, Argo CD syncs by enriching
// the entity another source owns. Each is placed on its service on every
// pass, from the deployments kept for the window, so a deployment kept
// before a rule could place it is placed by the next pass. Only what changed
// is written.
func (e *Engine) pushOutcomes(ctx context.Context) {
	allowed, ok := e.offer.ready(ctx)
	if !ok || !allowed(catalog.Deployment) {
		return
	}
	ctx, span := e.o.Tel.Start(ctx, "redgreen.outcomes")
	now := time.Now()
	e.mu.Lock()
	keys := make([]string, 0, len(e.repos))
	for k := range e.repos {
		keys = append(keys, k)
	}
	e.mu.Unlock()

	var records []outcomes.Record
	forge := map[string]forgeDeployment{}
	// settled holds the repositories whose deployments were all read and placed
	// with nothing left in doubt, so what they no longer make can be removed.
	settled := map[string]bool{}
	placed := outcomes.Placed{}
	for _, k := range keys {
		kept, err := state.Deployments[forgeDeployment](ctx, e.o.Store, k)
		if err != nil {
			e.o.Log.WarnContext(ctx, "kept deployments unreadable", "repo", k, "error", err)
			continue
		}
		t := e.get(k)
		var own []outcomes.Record
		dropped := 0
		for _, d := range kept {
			if now.Sub(d.FinishedAt) > window {
				continue
			}
			if d.Source == pipelineSource && !declaresPipeline(t, d.Service) {
				dropped++
				continue
			}
			d.Record.Job = d.Deployment.Job
			forge[d.Key] = d
			own = append(own, d.Record)
		}
		if dropped > 0 {
			e.o.Log.InfoContext(ctx, "pipeline deployments left out; their service no longer deploys by a pipeline", "repo", k, "deployments", dropped)
		}
		var targets []outcomes.Target
		if t != nil {
			targets = e.targets(k, t.found)
		}
		paths, doubt := e.pathsOf(ctx, t, k)
		list, counts := outcomes.Attribute(own, targets, paths)
		for why, n := range counts {
			placed[why] += n
		}
		records = append(records, list...)
		settled[k] = t != nil && t.found != nil && !*doubt
	}
	e.o.Log.InfoContext(ctx, "deployments placed on services", "placed", placed)
	var incidents []outcomes.Incident
	argo := map[string]bool{}
	// By the forge job's address, the entity another source keeps for it.
	elsewhere := map[string]string{}
	if e.o.Read != nil {
		if reports, err := e.o.Read.Reported(ctx, now.Add(-window)); err != nil {
			e.o.Log.WarnContext(ctx, "deployments other sources report unreadable; forge deployments are written as redgreen's own", "error", err)
		} else {
			handover := outcomes.HandOver(reports, func(key string) bool { _, ok := forge[key]; return ok })
			elsewhere = handover.Elsewhere
			e.o.Log.InfoContext(ctx, "deployments other sources report read", "reports", len(reports), "forgeJobs", len(elsewhere), "ownCopies", len(handover.Copies))
			e.dropCopies(ctx, handover.Copies)
		}
		syncs, apps, workloads, raw, err := e.o.Read.Read(ctx, now.Add(-window))
		if err != nil {
			e.o.Log.ErrorContext(ctx, "orrery read failed; outcomes count forge deployments alone", "error", err)
		} else {
			found, mapping := outcomes.MapSyncs(syncs, apps, workloads, e.places(ctx, keys), e.o.GitLabHost)
			for _, c := range mapping.Conflicts {
				e.o.Log.WarnContext(ctx, "an ARGOCD_SERVICES link and a service's orrery.yaml place one Argo CD app on different services; the link wins", "app", c.App, "linked", c.Linked, "declared", c.Declared)
			}
			for _, link := range mapping.Unmatched {
				e.o.Log.WarnContext(ctx, "ARGOCD_SERVICES names an Argo CD app or source the hub holds no app for; it places nothing", "link", link)
			}
			var dropped outcomes.Skipped
			incidents, dropped = outcomes.MapIncidents(raw, workloads, e.o.GitLabHost)
			withService := 0
			for _, r := range found {
				argo[r.Key] = true
				if r.Service != "" {
					withService++
				}
			}
			records = append(records, found...)
			e.o.Log.InfoContext(ctx, "orrery deployments read", "syncs", len(syncs), "apps", len(apps), "kept", len(found), "onAService", withService, "placedBy", mapping.Placed, "skipped", mapping.Skipped, "incidents", len(incidents), "incidentsSkipped", dropped)
		}
	}

	carried := e.carried(ctx, records)
	results := outcomes.Classify(records, carried, incidents)

	byScope := map[string][]sdk.Entity{}
	var enrich []sdk.Entity
	made := map[string]bool{}
	for _, r := range records {
		o := results[r.Key]
		t := e.get(r.RepoKey)
		from := r.Key
		if r.From != "" {
			from = r.From
		}
		d, isForge := forge[from]
		other := ""
		if isForge && d.Deployment.URL != "" {
			other = elsewhere[d.Deployment.URL]
		}
		var x sdk.Entity
		var scope string
		switch {
		case other != "" && r.From != "":
			// Another source keeps this job's one entity; the services past the first go without.
			continue
		case other != "":
			// Another source reports this job: its entity takes the outcome.
			x = sdk.Entity{Blueprint: catalog.Deployment, Key: other, Fields: o.Fields(), Relations: map[string]any{"repository": r.RepoKey}}
		case argo[r.Key]:
			rel := map[string]any{"repository": r.RepoKey}
			if r.Service != "" {
				rel["service"] = r.Service
			}
			x = sdk.Entity{Blueprint: catalog.Deployment, Key: r.Key, Fields: o.Fields(), Relations: rel}
		default:
			if !isForge || t == nil {
				continue
			}
			name := r.RepoKey
			if t.repo != nil {
				name = t.repo.Name
			}
			if r.Folder != "" {
				name += " / " + r.Folder
			}
			dep := d.Deployment
			finished := r.FinishedAt
			dep.Status, dep.FinishedAt = r.Status, &finished
			x = catalog.DeploymentEntity(r.Key, r.RepoKey, name+" to "+r.Environment, r.Service, dep, o)
			scope = accountScope(t.account) + "/deployments"
		}
		made[x.Key] = true
		if _, changed := e.changed(ctx, x); !changed {
			continue
		}
		if argo[r.Key] || other != "" {
			enrich = append(enrich, x)
		} else {
			byScope[scope] = append(byScope[scope], x)
		}
	}
	for scope, list := range byScope {
		e.runRemembered(ctx, scope, list)
	}
	e.runRemembered(ctx, argoScope, enrich, sdk.EnrichOnly())
	e.dropUnmade(ctx, made, settled)
	e.o.Log.InfoContext(ctx, "outcomes written", "deployments", len(records), "argo", len(argo), "incidents", len(incidents), "enriched", len(enrich))
	span.End()
}

// declaresPipeline says whether a tracked repository's service still says it
// deploys by a pipeline. Until its services are read, it is taken at its word.
func declaresPipeline(t *tracked, service string) bool {
	if t == nil || t.found == nil {
		return true
	}
	d := t.found.Describing(service)
	return d != nil && d.Deploys != nil && d.Deploys.Pipeline != ""
}

// pathsOf answers the files a range of one repository changed: from the
// state, or once from its forge, a bounded number per pass. doubt is set when
// a read failed, so a deployment it would have placed is not taken as unplaced.
func (e *Engine) pathsOf(ctx context.Context, t *tracked, repoKey string) (outcomes.PathsOf, *bool) {
	budget := pathReadsPerRepo
	doubt := new(bool)
	return func(rng outcomes.Range) []string {
		kept, err := e.o.Store.Paths(ctx, repoKey, rng.Base, rng.Head)
		if err != nil {
			*doubt = true
			e.o.Log.WarnContext(ctx, "kept changed files unreadable", "repo", repoKey, "error", err)
			return nil
		}
		if kept != nil {
			return kept
		}
		if t == nil || t.repo == nil {
			return nil
		}
		if budget == 0 {
			*doubt = true
			return nil
		}
		budget--
		got, err := e.provider(t.account.Provider).PathsBetween(ctx, t.account, t.repo, rng.Base, rng.Head)
		if err != nil {
			*doubt = true
			e.o.Log.WarnContext(ctx, "changed files could not be read; tried again next pass", "repo", t.repo.FullName, "base", rng.Base, "head", rng.Head, "error", err)
			return nil
		}
		if got == nil {
			e.o.Log.InfoContext(ctx, "changed files unknown; the deployment is not placed by them", "repo", t.repo.FullName, "base", rng.Base, "head", rng.Head)
			got = []string{}
		}
		if err := e.o.Store.PutPaths(ctx, repoKey, rng.Base, rng.Head, got, time.Now()); err != nil {
			e.o.Log.WarnContext(ctx, "changed files not kept", "repo", t.repo.FullName, "error", err)
		}
		return got
	}, doubt
}

// places is what places Argo CD syncs: the operator's links, the sources
// each tracked service's orrery.yaml names, and each tracked repository's
// services.
func (e *Engine) places(ctx context.Context, keys []string) outcomes.Places {
	var declared []outcomes.Declared
	for _, k := range keys {
		t := e.get(k)
		if t == nil || t.found == nil {
			continue
		}
		targets := e.targets(k, t.found)
		for _, d := range t.found.Descriptions {
			if d.Deploys == nil {
				continue
			}
			for _, src := range d.Deploys.ArgoCD {
				ref, ok := outcomes.ParseRepoURL(src, e.o.GitLabHost)
				if !ok {
					e.o.Log.WarnContext(ctx, "orrery.yaml names an Argo CD source that is no repository; left out", "repo", k, "service", d.Service, "source", src)
					continue
				}
				for _, target := range targets {
					if target.Key == d.Service {
						declared = append(declared, outcomes.Declared{From: src, Source: ref, RepoKey: k, Target: target})
					}
				}
			}
		}
	}
	return outcomes.Places{Linked: e.linked(ctx, keys), Declared: declared, Targets: func(key string) []outcomes.Target {
		t := e.get(key)
		if t == nil {
			return nil
		}
		return e.targets(key, t.found)
	}}
}

// linked is the operator's ARGOCD_SERVICES links, each tied to the service
// it names. A link to a service no watched repository holds is logged and
// left out; one whose repository is not read yet waits for the next pass.
func (e *Engine) linked(ctx context.Context, keys []string) []outcomes.Declared {
	if len(e.o.ArgoServices) == 0 {
		return nil
	}
	type at struct {
		repo   string
		target outcomes.Target
	}
	found := map[string]at{}
	unread := map[string]bool{}
	for _, k := range keys {
		t := e.get(k)
		if t == nil || t.found == nil {
			unread[k] = true
			continue
		}
		for _, target := range e.targets(k, t.found) {
			found[strings.ToLower(target.Key)] = at{k, target}
		}
	}
	froms := make([]string, 0, len(e.o.ArgoServices))
	for from := range e.o.ArgoServices {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	var out []outcomes.Declared
	for _, from := range froms {
		service := strings.ToLower(strings.Trim(e.o.ArgoServices[from], "/"))
		s, ok := found[service]
		if !ok {
			if repo := ownerOf(service+"/", unread); repo != "" {
				e.o.Log.InfoContext(ctx, "ARGOCD_SERVICES link waits for its service's repository to be read", "link", from, "service", service, "repo", repo)
			} else {
				e.o.Log.WarnContext(ctx, "ARGOCD_SERVICES names a service no watched repository holds; the link is skipped", "link", from, "service", service)
			}
			continue
		}
		d := outcomes.Declared{From: from, RepoKey: s.repo, Target: s.target}
		if strings.Contains(from, "://") {
			ref, ok := outcomes.ParseRepoURL(from, e.o.GitLabHost)
			if !ok {
				e.o.Log.WarnContext(ctx, "ARGOCD_SERVICES names a source that is no repository's web address; the link is skipped", "link", from)
				continue
			}
			d.Source = ref
		} else {
			d.App = from
		}
		out = append(out, d)
	}
	return out
}

// dropUnmade deletes the deployments redgreen made under a key of its own
// shape (`<key>@<service>`, `<service>@run-<id>`) that this pass did not make
// again: a service gone from a deployment, or a service that stopped
// deploying by its pipeline. Only a repository whose deployments were all
// read and placed is judged, so a failed read deletes nothing.
func (e *Engine) dropUnmade(ctx context.Context, made, settled map[string]bool) {
	written, err := e.o.Store.WrittenKeys(ctx, catalog.Deployment, "@")
	if err != nil {
		e.o.Log.WarnContext(ctx, "written deployments unreadable; none removed this pass", "error", err)
		return
	}
	var gone []string
	for _, key := range written {
		if made[key] {
			continue
		}
		if owner := ownerOf(key, settled); owner != "" && settled[owner] && ownShape(key, owner) {
			gone = append(gone, key)
		}
	}
	if len(gone) == 0 {
		return
	}
	deleted := 0
	for _, key := range gone {
		var missing *sdk.APIError
		if err := e.o.Push.Delete(ctx, catalog.Deployment, key); err != nil && !(errors.As(err, &missing) && missing.Status == http.StatusNotFound) {
			e.o.Log.WarnContext(ctx, "deployment no longer made not deleted; tried again next pass", "deployment", key, "error", err)
			continue
		}
		if err := e.o.Store.DeleteWritten(ctx, catalog.Deployment, key); err != nil {
			e.o.Log.WarnContext(ctx, "deleted deployment not forgotten", "deployment", key, "error", err)
		}
		deleted++
	}
	e.o.Log.InfoContext(ctx, "deployments no longer made deleted", "deleted", deleted, "of", len(gone))
}

// ownShape says whether a key under repo is one redgreen makes: a service's
// pipeline deployment, `<service>@run-<id>`, or a forge deployment's record
// for a service past its first, `<repo>/<id>@<folder>`. Another source's key
// that redgreen enriched is never one.
func ownShape(key, repo string) bool {
	rest := key[len(repo):]
	if strings.HasPrefix(rest, "@run-") {
		return true
	}
	id, after, ok := strings.Cut(strings.TrimPrefix(rest, "/"), "@")
	if !ok || !strings.HasPrefix(rest, "/") {
		return false
	}
	return strings.HasPrefix(after, "run-") || (id != "" && !strings.Contains(id, "/"))
}

// ownerOf is the repository a key of redgreen's own shape is under: the
// longest repository key it starts with, followed by "/" or "@".
func ownerOf(key string, repos map[string]bool) string {
	best := ""
	for repo := range repos {
		if len(repo) > len(best) && (strings.HasPrefix(key, repo+"/") || strings.HasPrefix(key, repo+"@")) {
			best = repo
		}
	}
	return best
}

// dropCopies deletes redgreen's own entities for forge jobs another source
// reports, and forgets what it wrote of them. One already gone counts as
// deleted; one that fails stays in the hub, so the next pass finds it again.
func (e *Engine) dropCopies(ctx context.Context, keys []string) {
	deleted := 0
	for _, key := range keys {
		var gone *sdk.APIError
		if err := e.o.Push.Delete(ctx, catalog.Deployment, key); err != nil && !(errors.As(err, &gone) && gone.Status == http.StatusNotFound) {
			e.o.Log.WarnContext(ctx, "own copy of a deployment another source reports not deleted; tried again next pass", "deployment", key, "error", err)
			continue
		}
		if err := e.o.Store.DeleteWritten(ctx, catalog.Deployment, key); err != nil {
			e.o.Log.WarnContext(ctx, "deleted deployment not forgotten", "deployment", key, "error", err)
		}
		deleted++
	}
	if len(keys) > 0 {
		e.o.Log.InfoContext(ctx, "own copies of deployments other sources report deleted", "deleted", deleted, "of", len(keys))
	}
}

// runRemembered writes deployments and remembers what took, so an unchanged
// one is not written again.
func (e *Engine) runRemembered(ctx context.Context, scope string, list []sdk.Entity, opts ...sdk.RunOption) {
	if len(list) == 0 {
		return
	}
	res, err := e.o.Push.Incremental(ctx, scope, func(add func(sdk.Entity) error) error {
		for _, x := range list {
			if err := add(x); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
	if err != nil {
		e.o.Tel.Pushed(ctx, catalog.Deployment, "failed", len(list))
		e.o.Log.ErrorContext(ctx, "orrery push failed", "blueprint", catalog.Deployment, "scope", scope, "entities", len(list), "error", err)
		return
	}
	refused := map[string]bool{}
	for _, w := range res.Errors {
		refused[w.Key] = true
	}
	for _, x := range list {
		if refused[x.Key] {
			continue
		}
		h, _ := e.changed(ctx, x)
		_ = e.o.Store.PutWritten(ctx, x.Blueprint, x.Key, h)
	}
	e.o.Tel.Pushed(ctx, catalog.Deployment, "rejected", res.Counts.Rejected)
	e.o.Tel.Pushed(ctx, catalog.Deployment, "accepted", len(list)-res.Counts.Rejected)
	e.o.Log.InfoContext(ctx, "orrery push done", "blueprint", catalog.Deployment, "scope", scope, "entities", len(list), "created", res.Counts.Created, "updated", res.Counts.Updated, "rejected", res.Counts.Rejected)
}

// carried reads what each deployment carried: from the state, or once
// through the forge's compare, a bounded number per repository per pass. A
// kept range whose base is no longer the stream's previous deployment, as
// when a deployment was placed on its service after it was read, is read
// again within the same bound.
func (e *Engine) carried(ctx context.Context, records []outcomes.Record) map[string]*outcomes.Carried {
	out := map[string]*outcomes.Carried{}
	prev := outcomes.Previous(records)
	byRepo := map[string][]outcomes.Record{}
	for _, r := range records {
		byRepo[r.RepoKey] = append(byRepo[r.RepoKey], r)
	}
	for key, list := range byRepo {
		t := e.get(key)
		budget := commitReadsPerRepo
		// The newest first, so the budget reaches the recent windows first.
		sort.SliceStable(list, func(i, j int) bool { return list[i].FinishedAt.After(list[j].FinishedAt) })
		for _, r := range list {
			kept, err := e.o.Store.Commits(ctx, r.Key)
			if err != nil {
				continue
			}
			var rng outcomes.Range
			known := false
			if t != nil && t.repo != nil {
				rng, known = outcomes.RangeOf(r, prev[r.Key], t.repo.Releases)
			}
			if kept != nil {
				var c outcomes.Carried
				ok := json.Unmarshal(kept.Facts, &c) == nil
				moved := known && rng.Base != "" && rng.Base != kept.Base
				if ok && (!moved || budget == 0) {
					out[r.Key] = &c
					continue
				}
			}
			if t == nil || t.repo == nil || budget == 0 {
				continue
			}
			c, err := e.read(ctx, t, r, rng, known)
			if err != nil {
				e.o.Log.WarnContext(ctx, "commits between revisions could not be read; tried again next pass", "repo", key, "deployment", r.Key, "error", err)
				continue
			}
			budget--
			if err := e.o.Store.PutCommits(ctx, r.Key, c.Base, c, time.Now()); err != nil {
				e.o.Log.WarnContext(ctx, "commits not kept", "deployment", r.Key, "error", err)
			}
			out[r.Key] = c
		}
	}
	return out
}

// read reads the range a deployment carried from its forge.
func (e *Engine) read(ctx context.Context, t *tracked, r outcomes.Record, rng outcomes.Range, known bool) (*outcomes.Carried, error) {
	if !known {
		e.o.Log.InfoContext(ctx, "deployment carries no known release; its commits stay unknown", "repo", t.repo.FullName, "deployment", r.Key)
		return &outcomes.Carried{Unknown: true}, nil
	}
	if rng.Base == rng.Head {
		return &outcomes.Carried{Base: rng.Base, Commits: []provider.Commit{}}, nil
	}
	p := e.provider(t.account.Provider)
	commits, err := p.CommitsBetween(ctx, t.account, t.repo, rng.Base, rng.Head)
	if err != nil {
		return nil, err
	}
	if commits == nil {
		e.o.Log.InfoContext(ctx, "commits between revisions unknown; the deployment counts without a lead time", "repo", t.repo.FullName, "deployment", r.Key, "base", rng.Base, "head", rng.Head)
		return &outcomes.Carried{Base: rng.Base, Unknown: true}, nil
	}
	return &outcomes.Carried{Base: rng.Base, Commits: commits}, nil
}
