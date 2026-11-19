package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	gosync "sync"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
)

// offer brings the blueprints once per start. A blueprint the hub refuses is
// not written; the whole offer is tried again on the next push when it failed.
type offer struct {
	client  *sdk.Client
	log     *slog.Logger
	mu      gosync.Mutex
	done    bool
	refused map[string]bool
}

// ready offers the blueprints when they have not been taken yet, and says
// which ones may be written.
func (o *offer) ready(ctx context.Context) (func(string) bool, bool) {
	if o.client == nil {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.done {
		core := catalog.Blueprints()
		res, err := o.client.OfferBlueprints(ctx, core[:len(core)-1])
		if res == nil {
			o.log.ErrorContext(ctx, "orrery blueprint offer failed; tried again on the next push", "error", err)
			return nil, false
		}
		refused := map[string]bool{}
		for _, r := range res.Rejected {
			refused[r.Blueprint] = true
			o.log.ErrorContext(ctx, "orrery refused a blueprint; its entities are not written", "blueprint", r.Blueprint, "reason", r.Reason)
		}
		// A team's DORA reads the standard's sw_service.team, which an estate may not have.
		if _, err := o.client.OfferBlueprints(ctx, []sdk.Blueprint{catalog.TeamDORA()}); err != nil {
			o.log.WarnContext(ctx, "orrery refused the team DORA fields; teams go without them", "error", err)
		}
		o.done, o.refused = true, refused
		o.log.InfoContext(ctx, "orrery blueprints offered", "created", res.Created, "extended", res.Extended, "kept", res.Kept, "refused", len(refused))
	}
	refused := o.refused
	return func(bp string) bool { return !refused[bp] }, true
}

func accountScope(a provider.Account) string {
	return strings.ToLower(string(a.Provider) + "/" + a.Login)
}

// written is what one repository writes, by blueprint. bare is each placed
// service without its place, by key, written where the hub refuses the place.
type written struct {
	repository  sdk.Entity
	pipelines   []sdk.Entity
	services    []sdk.Entity
	bare        map[string]sdk.Entity
	apis        []sdk.Entity
	connections []sdk.Entity
	issues      []sdk.Entity
}

// entities shapes one repository from its last read. ok is false when it has none.
func (e *Engine) entities(ctx context.Context, key string, now time.Time) (written, bool) {
	t := e.get(key)
	if t == nil || t.repo == nil {
		return written{}, false
	}
	r := t.repo
	out := written{repository: catalog.RepositoryEntity(r, t.found, now)}
	release := catalog.JudgeRelease(r.Pipelines)
	for _, p := range r.Pipelines {
		history, err := state.Runs[provider.Run](ctx, e.o.Store, catalog.PipelineKey(key, p.ID))
		if err != nil {
			e.o.Log.WarnContext(ctx, "kept runs unreadable; judged from the forge's list alone", "repo", r.FullName, "error", err)
		}
		h := catalog.Judge(p, r.DefaultBranch, history, release, e.o.Flaky, now)
		e.o.Log.DebugContext(ctx, "pipeline judged", "repo", r.FullName, "pipeline", p.ID, "flaky", h.Flaky, "flakyClearsInRuns", h.FlakyClearsIn)
		out.pipelines = append(out.pipelines, catalog.PipelineEntity(r, p, h))
	}
	if t.found != nil {
		out.services, out.bare = catalog.ServiceEntities(r, t.found, e.logos(ctx, r, t.found))
		out.apis = catalog.APIEntities(r, t.found)
		out.connections = catalog.ConnectionEntities(r, t.found)
	}
	out.issues = e.issues(ctx, key, r, now)
	return out, true
}

// logos puts each logo a repository's orrery.yaml files name when its
// bytes changed since the last put, and answers the names the hub holds. A
// failed put is logged and its name left out, so that service is written
// without an icon and keeps whatever icon the hub has.
func (e *Engine) logos(ctx context.Context, r *provider.Repo, found *services.Found) map[string]string {
	held := map[string]string{}
	for _, d := range found.Descriptions {
		icon := d.Logo
		if icon == nil {
			continue
		}
		if had, _ := e.o.Store.Written(ctx, "icon", icon.Name); had == icon.SHA256 {
			held[icon.Name] = icon.Name
			continue
		}
		if err := e.o.Push.PutIcon(ctx, icon.Name, icon.Type, icon.Data); err != nil {
			e.o.Log.WarnContext(ctx, "orrery icon put failed; the service is written without an icon", "repo", r.FullName, "icon", icon.Name, "error", err)
			continue
		}
		_ = e.o.Store.PutWritten(ctx, "icon", icon.Name, icon.SHA256)
		e.o.Log.InfoContext(ctx, "orrery icon put", "repo", r.FullName, "service", d.Service, "icon", icon.Name, "bytes", len(icon.Data))
		held[icon.Name] = icon.Name
	}
	return held
}

// issues are a repository's open issues and pull requests, and the ones that
// left the open list since the last read, marked closed. An archived
// repository's lists are not read, so it closes none.
func (e *Engine) issues(ctx context.Context, key string, r *provider.Repo, now time.Time) []sdk.Entity {
	open := catalog.IssueEntities(r)
	kept, err := e.o.Store.OpenIssues(ctx, key)
	if err != nil {
		e.o.Log.WarnContext(ctx, "kept issues unreadable; none closed this time", "repo", r.FullName, "error", err)
	}
	seen := map[string]bool{}
	for _, i := range open {
		seen[i.Key] = true
		if err := e.o.Store.PutIssue(ctx, i.Key, key, i, nil); err != nil {
			e.o.Log.WarnContext(ctx, "issue not kept", "repo", r.FullName, "error", err)
		}
	}
	if r.Archived {
		return open
	}
	for _, k := range kept {
		if seen[k.Key] {
			continue
		}
		var was sdk.Entity
		if json.Unmarshal(k.Entity, &was) != nil {
			continue
		}
		closed := catalog.Closed(was, now)
		open = append(open, closed)
		if err := e.o.Store.PutIssue(ctx, k.Key, key, closed, &now); err != nil {
			e.o.Log.WarnContext(ctx, "closed issue not kept", "repo", r.FullName, "error", err)
		}
	}
	return open
}

// run writes entities in one run and counts the outcome. Each entity the hub
// refuses that bare holds by key is written again in the same run as bare
// holds it, so a snapshot still counts it seen.
func (e *Engine) run(ctx context.Context, mode sdk.RunMode, scope, blueprint string, entities []sdk.Entity, bare map[string]sdk.Entity) {
	if len(entities) == 0 && mode == sdk.Incremental {
		return
	}
	res, unplaced, err := e.write(ctx, mode, scope, entities, bare)
	if err != nil {
		e.o.Tel.Pushed(ctx, blueprint, "failed", len(entities))
		e.o.Log.ErrorContext(ctx, "orrery push failed", "blueprint", blueprint, "scope", scope, "mode", mode, "entities", len(entities), "error", err)
		return
	}
	rejected := max(res.Counts.Rejected-unplaced, 0)
	e.o.Tel.Pushed(ctx, blueprint, "rejected", rejected)
	e.o.Tel.Pushed(ctx, blueprint, "accepted", len(entities)-rejected)
	e.o.Log.InfoContext(ctx, "orrery push done", "blueprint", blueprint, "scope", scope, "mode", mode, "entities", len(entities),
		"created", res.Counts.Created, "updated", res.Counts.Updated, "rejected", rejected, "deleted", res.Counts.Deleted, "massDrop", res.MassDrop,
		"placed", len(bare)-unplaced, "unplaced", unplaced)
}

// write sends entities in one run and commits it, failing the run on any
// error so nothing is deleted. unplaced counts the entities written again
// as bare holds them.
func (e *Engine) write(ctx context.Context, mode sdk.RunMode, scope string, entities []sdk.Entity, bare map[string]sdk.Entity) (res *sdk.RunResult, unplaced int, err error) {
	run, err := e.o.Push.OpenRun(ctx, mode, scope)
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (*sdk.RunResult, int, error) {
		_ = run.Fail(ctx, err.Error())
		return nil, 0, fmt.Errorf("%s run of %s failed: %w", mode, scope, err)
	}
	for _, x := range entities {
		if err := run.Add(ctx, x); err != nil {
			return fail(err)
		}
	}
	if len(bare) > 0 {
		if err := run.Flush(ctx); err != nil {
			return fail(err)
		}
		again := map[string]bool{}
		for _, w := range run.Rejected() {
			b, ok := bare[w.Key]
			if !ok || again[w.Key] {
				continue
			}
			again[w.Key] = true
			e.o.Log.WarnContext(ctx, "orrery refused a service's place; it stands without it", "service", w.Key, "field", w.Field, "reason", w.Reason)
			if err := run.Add(ctx, b); err != nil {
				return fail(err)
			}
		}
		unplaced = len(again)
	}
	res, err = run.Commit(ctx)
	return res, unplaced, err
}

// pushRepo writes one repository right after a webhook read it, without
// deleting anything: the next sweep's snapshot settles the account.
func (e *Engine) pushRepo(ctx context.Context, key string) {
	allowed, ok := e.offer.ready(ctx)
	if !ok {
		return
	}
	t := e.get(key)
	w, ok := e.entities(ctx, key, time.Now())
	if t == nil || !ok {
		return
	}
	scope := accountScope(t.account)
	if allowed(catalog.Repository) {
		e.run(ctx, sdk.Incremental, scope, catalog.Repository, []sdk.Entity{w.repository}, nil)
	}
	if allowed(catalog.Pipeline) {
		e.run(ctx, sdk.Incremental, scope+"/pipelines", catalog.Pipeline, w.pipelines, nil)
	}
	if allowed(catalog.Service) {
		e.run(ctx, sdk.Incremental, scope+"/services", catalog.Service, w.services, w.bare)
	}
	if allowed(catalog.API) {
		e.run(ctx, sdk.Incremental, scope+"/apis", catalog.API, w.apis, nil)
	}
	if allowed(catalog.Connection) {
		e.run(ctx, sdk.Incremental, scope+"/connections", catalog.Connection, w.connections, nil)
	}
	if allowed(catalog.Issue) {
		e.run(ctx, sdk.Incremental, scope+"/issues", catalog.Issue, w.issues, nil)
	}
}

// pushAccount writes every repository of one account. Each blueprint goes as
// a snapshot when every repository read cleanly, so what left the forge
// leaves the catalog; otherwise as an incremental run, so nothing half read is
// deleted. Issues only accumulate and leave by age.
func (e *Engine) pushAccount(ctx context.Context, a provider.Account, keys []string) {
	allowed, ok := e.offer.ready(ctx)
	if !ok {
		return
	}
	now := time.Now()
	var repos, pipelines, svcs, apis, connections, issues []sdk.Entity
	bare := map[string]sdk.Entity{}
	clean, servicesRead := true, true
	for _, k := range keys {
		t := e.get(k)
		if t == nil || !t.ok {
			clean = false
		}
		if t != nil && t.found == nil {
			servicesRead = false
		}
		w, ok := e.entities(ctx, k, now)
		if !ok {
			clean = false
			continue
		}
		repos = append(repos, w.repository)
		pipelines = append(pipelines, w.pipelines...)
		svcs = append(svcs, w.services...)
		maps.Copy(bare, w.bare)
		apis = append(apis, w.apis...)
		connections = append(connections, w.connections...)
		issues = append(issues, w.issues...)
	}
	mode := sdk.Snapshot
	if !clean {
		mode = sdk.Incremental
		e.o.Log.InfoContext(ctx, "account written without deleting; some repos did not read", "account", a.Login)
	}
	scope := accountScope(a)
	if allowed(catalog.Repository) {
		e.run(ctx, mode, scope, catalog.Repository, repos, nil)
	}
	if allowed(catalog.Pipeline) {
		e.run(ctx, mode, scope+"/pipelines", catalog.Pipeline, pipelines, nil)
	}
	if allowed(catalog.Service) {
		serviceMode := mode
		if !servicesRead {
			serviceMode = sdk.Incremental
		}
		e.run(ctx, serviceMode, scope+"/services", catalog.Service, svcs, bare)
		if allowed(catalog.API) {
			e.run(ctx, serviceMode, scope+"/apis", catalog.API, apis, nil)
		}
		if allowed(catalog.Connection) {
			e.run(ctx, serviceMode, scope+"/connections", catalog.Connection, connections, nil)
		}
	}
	if allowed(catalog.Issue) {
		e.run(ctx, sdk.Incremental, scope+"/issues", catalog.Issue, issues, nil)
	}
}

// changed says whether an entity differs from what was last written for it.
func (e *Engine) changed(ctx context.Context, x sdk.Entity) (string, bool) {
	b, _ := json.Marshal(x)
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	had, err := e.o.Store.Written(ctx, x.Blueprint, x.Key)
	return h, err != nil || had != h
}
