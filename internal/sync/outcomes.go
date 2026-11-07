package sync

import (
	"context"
	"encoding/json"
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

// argoScope is where the outcomes added to Argo CD syncs are written.
const argoScope = "argo-outcomes"

// pushOutcomes works out every deployment's outcome and writes it: forge
// deployments as entities of their own, Argo CD syncs by enriching the
// entity another source owns. Only what changed is written.
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
	for _, k := range keys {
		kept, err := state.Deployments[forgeDeployment](ctx, e.o.Store, k)
		if err != nil {
			e.o.Log.WarnContext(ctx, "kept deployments unreadable", "repo", k, "error", err)
			continue
		}
		for _, d := range kept {
			if now.Sub(d.FinishedAt) > window {
				continue
			}
			forge[d.Key] = d
			records = append(records, d.Record)
		}
	}
	var incidents []outcomes.Incident
	argo := map[string]bool{}
	if e.o.Read != nil {
		syncs, apps, workloads, raw, err := e.o.Read.Read(ctx, now.Add(-window))
		if err != nil {
			e.o.Log.ErrorContext(ctx, "orrery read failed; outcomes count forge deployments alone", "error", err)
		} else {
			tracked := func(r outcomes.RepoRef) bool { return e.get(r.Key()) != nil }
			found, skipped := outcomes.MapSyncs(syncs, apps, workloads, tracked, e.o.GitLabHost)
			var dropped outcomes.Skipped
			incidents, dropped = outcomes.MapIncidents(raw, workloads, e.o.GitLabHost)
			for _, r := range found {
				argo[r.Key] = true
			}
			records = append(records, found...)
			e.o.Log.InfoContext(ctx, "orrery deployments read", "syncs", len(syncs), "kept", len(found), "skipped", skipped, "incidents", len(incidents), "incidentsSkipped", dropped)
		}
	}

	carried := e.carried(ctx, records)
	results := outcomes.Classify(records, carried, incidents)

	byScope := map[string][]sdk.Entity{}
	var enrich []sdk.Entity
	for _, r := range records {
		o := results[r.Key]
		t := e.get(r.RepoKey)
		service := ""
		if t != nil {
			service = catalog.DeploymentService(r.RepoKey, t.found, r.Folder)
		}
		var x sdk.Entity
		var scope string
		if argo[r.Key] {
			rel := map[string]any{"repository": r.RepoKey}
			if service != "" {
				rel["service"] = service
			}
			x = sdk.Entity{Blueprint: catalog.Deployment, Key: r.Key, Fields: o.Fields(), Relations: rel}
		} else {
			d, ok := forge[r.Key]
			if !ok || t == nil {
				continue
			}
			name := r.RepoKey
			if t.repo != nil {
				name = t.repo.Name
			}
			x = catalog.DeploymentEntity(r.RepoKey, name, service, d.Deployment, o)
			scope = accountScope(t.account) + "/deployments"
		}
		if _, changed := e.changed(ctx, x); !changed {
			continue
		}
		if argo[r.Key] {
			enrich = append(enrich, x)
		} else {
			byScope[scope] = append(byScope[scope], x)
		}
	}
	for scope, list := range byScope {
		e.runRemembered(ctx, scope, list)
	}
	e.runRemembered(ctx, argoScope, enrich, sdk.EnrichOnly())
	e.o.Log.InfoContext(ctx, "outcomes written", "deployments", len(records), "argo", len(argo), "incidents", len(incidents), "enriched", len(enrich))
	span.End()
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
// through the forge's compare, a bounded number per repository per pass.
func (e *Engine) carried(ctx context.Context, records []outcomes.Record) map[string]*outcomes.Carried {
	out := map[string]*outcomes.Carried{}
	byRepo := map[string][]outcomes.Record{}
	for _, r := range records {
		byRepo[r.RepoKey] = append(byRepo[r.RepoKey], r)
	}
	for key, list := range byRepo {
		t := e.get(key)
		budget := commitReadsPerRepo
		for _, r := range list {
			kept, err := e.o.Store.Commits(ctx, r.Key)
			if err != nil {
				continue
			}
			if kept != nil {
				var c outcomes.Carried
				if json.Unmarshal(kept.Facts, &c) == nil {
					out[r.Key] = &c
				}
				continue
			}
			if t == nil || t.repo == nil || budget == 0 {
				continue
			}
			c, err := e.read(ctx, t, r, list)
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

// read finds the range a deployment carried and reads it from its forge.
func (e *Engine) read(ctx context.Context, t *tracked, r outcomes.Record, stream []outcomes.Record) (*outcomes.Carried, error) {
	rng, ok := outcomes.RangeOf(r, stream, t.repo.Releases)
	if !ok {
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
