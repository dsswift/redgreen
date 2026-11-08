// Package sync keeps Orrery current with the forges. A sweep finds every
// allowed account and repository and reads each one on a pool of workers; a
// webhook reads one repository ahead of the sweep. After each read the
// repository's entities are written; after each sweep every account is
// written as a snapshot and the deployments' outcomes are worked out and
// written. Nothing here blocks the health check.
package sync

import (
	"context"
	"log/slog"
	"strings"
	gosync "sync"
	"time"

	"github.com/dsswift/orrery/sdk"
	"github.com/dsswift/redgreen/internal/catalog"
	cijobs "github.com/dsswift/redgreen/internal/jobs"
	"github.com/dsswift/redgreen/internal/outcomes"
	"github.com/dsswift/redgreen/internal/provider"
	"github.com/dsswift/redgreen/internal/services"
	"github.com/dsswift/redgreen/internal/state"
	"github.com/dsswift/redgreen/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// webhookDebounce is how long a webhook waits for more deliveries about the same repository.
const webhookDebounce = 3 * time.Second

// Options configure an Engine.
type Options struct {
	Providers   []provider.Provider
	Store       *state.Store
	Push        *sdk.Client
	Read        *outcomes.Reader
	Tel         *telemetry.Telemetry
	Log         *slog.Logger
	Bots        services.Bots
	Interval    time.Duration
	Concurrency int
	GitLabHost  string
}

// tracked is one repository as the last read left it.
type tracked struct {
	account provider.Account
	ref     provider.RepoRef
	repo    *provider.Repo
	found   *services.Found
	// ok is false when the last read failed.
	ok bool
}

// Engine is the sweep, the workers, and the pushes.
type Engine struct {
	o       Options
	offer   *offer
	jobs    chan job
	urgent  chan job
	sweepMu gosync.Mutex
	again   chan struct{}

	mu        gosync.Mutex
	repos     map[string]*tracked
	byName    map[string]string
	debounce  map[string]*time.Timer
	delivered map[string]time.Time

	jobsInFlight *cijobs.Tracker
}

type job struct {
	key  string
	done func(error)
}

// New builds an engine; Run starts it.
func New(o Options) *Engine {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	e := &Engine{
		o: o, jobsInFlight: cijobs.New(), offer: &offer{client: o.Push, log: o.Log},
		jobs: make(chan job, 1024), urgent: make(chan job, 256), again: make(chan struct{}, 1),
		repos: map[string]*tracked{}, byName: map[string]string{}, debounce: map[string]*time.Timer{}, delivered: map[string]time.Time{},
	}
	if o.Tel != nil {
		o.Tel.WatchJobs(e.activeJobs)
	}
	return e
}

func nameKey(kind provider.Kind, fullName string) string {
	return string(kind) + "\n" + strings.ToLower(fullName)
}

// Run starts the workers and sweeps now and on every interval until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	for range e.o.Concurrency {
		go e.work(ctx)
	}
	tick := time.NewTicker(e.o.Interval)
	defer tick.Stop()
	e.Sweep(ctx, "startup")
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			e.Sweep(ctx, "interval")
		case <-e.again:
			e.Sweep(ctx, "webhook")
		}
	}
}

func (e *Engine) work(ctx context.Context) {
	for {
		var j job
		select {
		case <-ctx.Done():
			return
		case j = <-e.urgent:
		default:
			select {
			case <-ctx.Done():
				return
			case j = <-e.urgent:
			case j = <-e.jobs:
			}
		}
		err := e.syncRepo(ctx, j.key)
		if j.done != nil {
			j.done(err)
		}
	}
}

// Sweep finds every account and repository, reads them all, writes every
// account, and works out the deployments' outcomes. A sweep already running
// is not doubled.
func (e *Engine) Sweep(ctx context.Context, reason string) {
	if !e.sweepMu.TryLock() {
		e.o.Log.InfoContext(ctx, "sweep already running; skipped", "reason", reason)
		return
	}
	defer e.sweepMu.Unlock()
	ctx, span := e.o.Tel.Root(ctx, "redgreen.sweep", attribute.String("task", "sweep"), attribute.String("reason", reason))
	started := time.Now()
	e.o.Log.InfoContext(ctx, "sweep started", "reason", reason)
	complete := true
	byAccount := map[string][]string{}
	accounts := map[string]provider.Account{}
	for _, p := range e.o.Providers {
		if !p.Configured() {
			continue
		}
		found, err := p.Accounts(ctx)
		if err != nil {
			complete = false
			e.o.Log.ErrorContext(ctx, "account discovery failed", "provider", p.Kind(), "error", err)
			continue
		}
		for _, a := range found {
			refs, err := p.Repos(ctx, a)
			if err != nil {
				complete = false
				e.o.Log.ErrorContext(ctx, "repository discovery failed", "provider", p.Kind(), "account", a.Login, "error", err)
				continue
			}
			accounts[a.ID] = a
			byAccount[a.ID] = e.track(a, refs)
			e.o.Log.InfoContext(ctx, "account discovered repos", "provider", p.Kind(), "account", a.Login, "repos", len(refs))
		}
	}
	var wg gosync.WaitGroup
	for _, keys := range byAccount {
		for _, k := range keys {
			wg.Add(1)
			e.jobs <- job{key: k, done: func(error) { wg.Done() }}
		}
	}
	wg.Wait()
	for id, keys := range byAccount {
		e.pushAccount(ctx, accounts[id], keys)
	}
	e.pushOutcomes(ctx)
	if pruned, err := e.o.Store.Prune(ctx, time.Now()); err != nil {
		e.o.Log.WarnContext(ctx, "state prune failed", "error", err)
	} else {
		e.o.Log.InfoContext(ctx, "state pruned", "responses", pruned.Responses, "deliveries", pruned.Deliveries, "runs", pruned.Runs, "deployments", pruned.Deployments, "issues", pruned.Issues)
	}
	if complete {
		e.o.Tel.SweepSucceeded(time.Now())
	}
	e.o.Log.InfoContext(ctx, "sweep finished", "reason", reason, "complete", complete, "accounts", len(byAccount), "ms", time.Since(started).Milliseconds())
	telemetry.End(span, nil)
}

// track keeps the repositories an account holds and drops the ones it no longer does.
func (e *Engine) track(a provider.Account, refs []provider.RepoRef) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	var keys []string
	for _, r := range refs {
		k := catalog.RepoKey(a.Provider, r.FullName)
		seen[k] = true
		keys = append(keys, k)
		t, ok := e.repos[k]
		if !ok {
			t = &tracked{}
			e.repos[k] = t
		}
		t.account, t.ref = a, r
		e.byName[nameKey(a.Provider, r.FullName)] = k
	}
	for k, t := range e.repos {
		if t.account.ID == a.ID && !seen[k] {
			delete(e.repos, k)
		}
	}
	return keys
}

func (e *Engine) provider(kind provider.Kind) provider.Provider {
	for _, p := range e.o.Providers {
		if p.Kind() == kind {
			return p
		}
	}
	return nil
}

func (e *Engine) get(key string) *tracked {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.repos[key]
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// syncRepo reads one repository and keeps what outlives the read: its runs,
// its finished deployments, and its services.
func (e *Engine) syncRepo(ctx context.Context, key string) error {
	t := e.get(key)
	if t == nil {
		return nil
	}
	p := e.provider(t.account.Provider)
	ctx, span := e.o.Tel.Start(ctx, "redgreen.sync", attribute.String("provider", string(p.Kind())))
	started := time.Now()
	repo, err := p.Sync(ctx, t.account, t.ref)
	e.o.Tel.Synced(ctx, string(p.Kind()), time.Since(started), err)
	if err != nil {
		e.o.Log.ErrorContext(ctx, "repo sync failed", "repo", t.ref.FullName, "error", err)
		e.mu.Lock()
		if live := e.repos[key]; live != nil {
			live.ok = false
		}
		e.mu.Unlock()
		telemetry.End(span, err)
		return err
	}
	for _, pl := range repo.Pipelines {
		pk := catalog.PipelineKey(key, pl.ID)
		for _, r := range pl.Runs {
			if err := e.o.Store.PutRun(ctx, pk, r.ID, r.CreatedAt, r); err != nil {
				e.o.Log.WarnContext(ctx, "run not kept", "repo", repo.FullName, "error", err)
			}
		}
	}
	for _, d := range repo.Deployments {
		if d.FinishedAt == nil || (d.Status != provider.DeploySucceeded && d.Status != provider.DeployFailed) {
			continue
		}
		rec := forgeDeployment{Record: outcomes.Record{Key: key + "/" + d.ID, Source: "forge", RepoKey: key, Environment: d.Environment, Status: d.Status, Revision: d.SHA, FinishedAt: *d.FinishedAt}, Deployment: d}
		if err := e.o.Store.PutDeployment(ctx, rec.Key, key, *d.FinishedAt, rec); err != nil {
			e.o.Log.WarnContext(ctx, "deployment not kept", "repo", repo.FullName, "error", err)
		}
	}
	found := e.services(ctx, p, t, repo)
	e.mu.Lock()
	if live := e.repos[key]; live != nil {
		live.repo, live.found, live.ok = repo, found, true
	}
	e.mu.Unlock()
	e.o.Log.InfoContext(ctx, "repo synced", "repo", repo.FullName, "pipelines", len(repo.Pipelines), "ms", time.Since(started).Milliseconds())
	telemetry.End(span, nil)
	return nil
}

// forgeDeployment is a forge deployment as the state keeps it.
type forgeDeployment struct {
	outcomes.Record
	Deployment provider.Deployment `json:"deployment"`
}

// services reads the services at the head of the default branch. A failed
// read keeps the last answer, so nothing is dropped for a hiccup.
func (e *Engine) services(ctx context.Context, p provider.Provider, t *tracked, repo *provider.Repo) *services.Found {
	key := catalog.RepoKey(repo.Provider, repo.FullName)
	previous := t.found
	if previous == nil {
		var kept services.Found
		if ok, err := e.o.Store.Services(ctx, key, &kept); err == nil && ok {
			previous = &kept
		}
	}
	files, err := p.Files(ctx, t.account, repo)
	if err != nil {
		e.o.Log.WarnContext(ctx, "services could not be read; the last answer stands", "repo", repo.FullName, "error", err)
		return previous
	}
	found, out, err := services.Read(ctx, files, repo.Name, previous, e.o.Bots)
	if err != nil {
		e.o.Log.WarnContext(ctx, "services could not be read; the last answer stands", "repo", repo.FullName, "error", err)
		return previous
	}
	if out.Reused {
		return found
	}
	e.o.Log.InfoContext(ctx, "services read", "repo", repo.FullName, "commit", found.Commit, "services", len(found.Services), "filesRead", out.Read)
	if out.Read > services.ReadBudget {
		e.o.Log.WarnContext(ctx, "read budget spent; services found so far stand", "repo", repo.FullName, "budget", services.ReadBudget)
	}
	if found.IconProblem != "" {
		e.o.Log.WarnContext(ctx, "repo names a logo that cannot be used; its services wear their language", "repo", repo.FullName, "problem", found.IconProblem)
	}
	if err := e.o.Store.PutServices(ctx, key, found); err != nil {
		e.o.Log.WarnContext(ctx, "services not kept", "repo", repo.FullName, "error", err)
	}
	return found
}

// Webhook reads the repositories a delivery names, ahead of the sweep, and
// asks for a sweep when accounts or repository lists may have changed.
func (e *Engine) Webhook(ctx context.Context, kind provider.Kind, d provider.Delivery) {
	if d.ID != "" {
		seen, err := e.o.Store.Seen(ctx, string(kind)+":"+d.ID, time.Now())
		if err == nil && seen {
			e.o.Tel.Webhook(ctx, string(kind), "duplicate")
			e.o.Log.InfoContext(ctx, "webhook delivered twice; ignored", "provider", kind, "delivery", d.ID)
			return
		}
	}
	e.o.Tel.Webhook(ctx, string(kind), "accepted")
	e.o.Log.InfoContext(ctx, "webhook received", "provider", kind, "event", d.Event, "delivery", d.ID, "repos", d.Repos, "rediscover", d.Rediscover)
	if d.Job != nil {
		e.job(ctx, kind, *d.Job)
	}
	for _, name := range d.Repos {
		e.mu.Lock()
		key, ok := e.byName[nameKey(kind, name)]
		if ok {
			if _, waiting := e.delivered[key]; !waiting {
				e.delivered[key] = time.Now()
			}
			if t := e.debounce[key]; t != nil {
				t.Stop()
			}
			e.debounce[key] = time.AfterFunc(webhookDebounce, func() { e.refresh(key) })
		}
		e.mu.Unlock()
		if !ok {
			e.o.Log.InfoContext(ctx, "webhook named a repo that is not tracked", "provider", kind, "repo", name)
		}
	}
	if d.Rediscover {
		select {
		case e.again <- struct{}{}:
		default:
		}
	}
}

// job measures one CI job's change of state, for a tracked repository only.
func (e *Engine) job(ctx context.Context, kind provider.Kind, j provider.Job) {
	e.mu.Lock()
	_, ok := e.byName[nameKey(kind, j.Repo)]
	e.mu.Unlock()
	if !ok {
		e.o.Log.InfoContext(ctx, "job in a repo that is not tracked; not measured", "provider", kind, "repo", j.Repo, "job", j.ID)
		return
	}
	c := e.jobsInFlight.Observe(j, time.Now())
	if c.Started {
		e.o.Tel.JobStarted(ctx, c.Repo, c.Runner, c.OS, c.Wait)
	}
	if c.Finished {
		e.o.Tel.JobFinished(ctx, c.Repo, c.Runner, c.OS, c.Conclusion, c.Ran)
	}
}

func (e *Engine) activeJobs() []telemetry.ActiveJobs {
	counts := e.jobsInFlight.Active(time.Now())
	out := make([]telemetry.ActiveJobs, 0, len(counts))
	for _, c := range counts {
		out = append(out, telemetry.ActiveJobs{Repo: c.Repo, Runner: c.Runner, OS: c.OS, Status: c.Status, N: c.N})
	}
	return out
}

// refresh reads one repository ahead of the sweep and writes it.
func (e *Engine) refresh(key string) {
	ctx, span := e.o.Tel.Root(context.Background(), "redgreen.refresh", attribute.String("task", "webhook"))
	e.urgent <- job{key: key, done: func(err error) {
		defer telemetry.End(span, err)
		e.mu.Lock()
		at, had := e.delivered[key]
		delete(e.delivered, key)
		delete(e.debounce, key)
		e.mu.Unlock()
		if err != nil {
			return
		}
		e.pushRepo(ctx, key)
		if t := e.get(key); had && t != nil {
			e.o.Tel.WebhookLag(ctx, string(t.account.Provider), time.Since(at))
		}
	}}
}
