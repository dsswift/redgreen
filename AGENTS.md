# redgreen

The forges' door into Orrery. Read `README.md` first.

## Shape

- A headless Go service. Orrery is the board and the math; redgreen watches
  the forges and writes facts. It never stores what Orrery stores and never
  computes what Orrery can compute when a page is read.
- `cmd/redgreen` is the command: `serve`, `setup github`, `setup gitlab`, and
  `import-history`. Nothing else lives there but wiring.
- `internal/provider` is the neutral model (`model.go`) and the `Provider`
  interface. One package per forge under it (`github`, `gitlab`,
  `azuredevops`). Nothing outside a forge's package knows its JSON. Adding a
  forge means adding a package and listing it in `cmd/redgreen/serve.go`.
- Every forge read goes through `provider.Get`, the conditional request: it
  sends back the ETag, and a 304 reuses the facts kept from the first answer.
  `provider.Once` is for an answer never asked for again, such as a tree at
  one commit: it keeps nothing. Never call a forge around them.
- `internal/state` is the SQLite file (pure Go, `modernc.org/sqlite`). It
  keeps facts, never raw bodies: per URL the ETag and the normalised facts,
  webhook delivery ids, runs for 60 days, forge deployments and the commits
  each carried for 200 days, open issues, services per repository, and a
  hash of what was last written. Losing it costs one sweep.
- `internal/catalog` is what Orrery gets. `blueprints.go` offers the
  blueprints; `expressions.go` holds every calculation and is the only place
  one is written; `health.go` works out what only the runs can tell (a
  failing streak, a missed schedule, a failed release build, a flaky
  commit); `entities.go` maps a read onto entities. A field written must be
  defined in its blueprint, and a calculation reads only fields before it
  (`blueprints_test.go` checks both).
- `internal/outcomes` works out each deployment's lead time, commit count,
  fix-only flag, failed change, and restore time. Orrery aggregates them per
  service and per team. `orrery.go` reads Argo CD syncs, apps, workloads, and
  `it_incident` back from Orrery with `ORRERY_READ_TOKEN`; a workload's `repo`
  URL, read by `repourl.go`, places a sync or an incident in a repository and
  folder.
- `internal/services` finds services from the files that build them. `Detect`
  is the only place a service is recognised. Bump `Rules` with any change to
  a rule, so every repository is read again.
- `internal/sync` is the engine: the sweep, the workers, webhooks, and the
  writes. `internal/describe` writes redgreen's own `orrery.yaml`.
  `internal/telemetry` is logs, traces, and metrics.

## Rules of the house

- Health and DORA are Orrery calculations and aggregations. Never compute a
  status, a rate, or a band in Go; push the fact and let the blueprint say
  what it means. Run `scripts/check-expressions.mts` after changing one.
- A blueprint offered to a hub that already has it only adds what it lacks.
  A changed field definition or a new `status.field` does not reach that hub;
  say so in the commit.
- Health and push run on goroutines. `/healthz` never touches the state or a
  forge.
- Logs are JSON lines on stdout. Log both branches of any decision that
  changes what Orrery shows.
- Accounts sync only when allowed: `GITHUB_ALLOWED_ACCOUNTS`,
  `GITLAB_ALLOWED_ACCOUNTS`, or the organisations `AZURE_DEVOPS_ORGANIZATIONS`
  names. Keep that gate; the GitHub App is public so it installs on several
  orgs.
- Forge tokens are renewed inside their provider and never cached elsewhere.
  GitLab replaces its refresh token on every use, so the newest is kept in
  the state, sealed with `SECRET_KEY`.
- A service is written with what the files say: its folder, kind, language,
  README, Dockerfile, tests, and icon. What people decide about it is never
  pushed; a pushed value would fight their edits.
- `VERSION` is the version of record.

## Releases

Commit on `main` and land with `release-damnit land`. CI versions every `feat`
and `fix` since the last release tag, tags it, and builds the tag's image,
`ghcr.io/dsswift/redgreen`. Never edit `VERSION`, the manifest, or
`CHANGELOG.md` by hand. The image build reads the private Orrery module with
the `ORRERY_MODULE_TOKEN` secret.

## Checks

```bash
go vet ./...
go test ./...
go run ./scripts/blueprints > /tmp/blueprints.json && node scripts/check-expressions.mts /tmp/blueprints.json
```

The last one needs an Orrery checkout beside this repository with its
dependencies installed. Run all three before every commit.
