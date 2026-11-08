# redgreen

redgreen watches GitHub, GitLab, and Azure DevOps and writes what it finds
into an [Orrery](https://github.com/dsswift/orrery) catalog: repositories,
pipelines, services, deployments, and issues. Orrery is the board and the
math. Health and the four DORA measures are calculations and aggregations in
the blueprints redgreen offers, so Orrery works them out when a page is read,
and changing a rule never needs a resync.

redgreen has no web interface. It is one Go binary with a small SQLite state
that can be rebuilt by one sweep.

## What it writes

| Blueprint | Kind | What it carries |
| --- | --- | --- |
| `sw_repository` | inventory | forge, account, default branch, archived, visibility, language, last push, branch protection, alert counts, pull requests failing checks, days idle, the health rules, and a calculated `status` with `status_reason` |
| `sw_pipeline` | inventory | one per workflow, build definition, or schedule: the latest default-branch run, schedule state, newest release build, 30-day success rate, median duration, a 60-day success trend, and a flaky flag. Runs stay on the forge. |
| `sw_service` | inventory | each service found from the files that build it (folder, kind, language, icon, last human change, README, Dockerfile, tests), and its DORA measures |
| `sw_deployment` | history, kept 180 days | each deployment a forge recorded, with lead time, commits carried, fix only, failed change, and restore time. Argo CD syncs another source writes get the same fields added. |
| `sw_issue` | record, kept 90 days after closing | open issues and pull requests on every forge, linked to their repository |
| `team` | fields only | the same DORA measures over a team's services, through the standard's `sw_service.team` |

### Health

Each rule is a field. Its severity is a field too, with a blueprint default
for the whole estate, and a person can change it on one entity: `red`,
`amber`, `info` (shown, colours nothing), or `off`. `muted` greys out a
repository or a pipeline.

| Rule | On | Default |
| --- | --- | --- |
| `default_branch_failing`: the latest automatic run on the default branch failed | pipeline | red |
| `schedule_stale`: two expected scheduled runs did not come, or the forge switched the schedule off | pipeline | amber |
| `release_build_failed`: the run for the newest release or tag failed | pipeline | red |
| `flaky`: one commit both passed and failed in the last 30 days | pipeline | amber |
| `critical_alerts`: open critical dependency or code scanning alerts | repository | red |
| `pr_checks_failing`: an open, ready pull request has failing checks | repository | red |
| `archive_candidate`: idle 90 days or more, and red | repository | amber |

A repository's status is the worst of its own rules and its pipelines'
statuses, read through `<-sw_pipeline:repository`. Muted and disabled
pipelines count for nothing. A repository with no active pipeline and no
rule that holds is grey.

### DORA

On `sw_service` and `team`, over the deployments of the last 30 days and the
30 before them: deploys per day, lead time (median and 90th percentile of
`lead_time_hours`), change failure rate (failed changes over changes), and
time to restore (median of `restore_hours`), each with a band (elite, high,
medium, low) and `dora_grade`, the worst band.

redgreen works out per deployment only what needs a forge call or the
deployments around it:

- `lead_time_hours`: the median hours from each carried commit's authoring to
  the end of the deployment. The commits come from the forge's compare, from
  the last good deployment of the same stream (repository, folder,
  environment), read once.
- `fix_only`: every carried commit is a `fix`, a `revert`, or a forge's revert.
- `failed_change`: it failed, a fix-only deployment followed within a day, or
  a critical or warning `it_incident` opened on its workload within a day.
- `restore_hours`: to the next good deployment, the fix, or the incident's
  `mitigated_at`, else its `resolved_at`.

## Running it

```bash
redgreen serve
```

| Variable | What it does |
| --- | --- |
| `PORT` | HTTP port, 8080. |
| `DATA_DIR` | Where `state.sqlite` lives, `./data`. |
| `PUBLIC_URL` | Where forges reach the webhooks. |
| `SYNC_INTERVAL_MINUTES` | Minutes between sweeps, 5. |
| `SYNC_CONCURRENCY` | Repositories read at once, 4. |
| `BOT_AUTHORS` | More commit authors that count as automation, comma separated. |
| `ORRERY_URL` | The hub's machine surface. |
| `ORRERY_TOKEN` | A source token, registered for the blueprints above. |
| `ORRERY_PUSH_TOKEN` | A source token for `sw_service` and `sw_api`, for redgreen's own description. |
| `ORRERY_READ_TOKEN` | An access token that may read: Argo CD syncs, apps, workloads, and incidents. |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` | OTLP traces and metrics; off when unset. |
| `LOG_LEVEL` | `debug`, `info`, `warn`, or `error`. |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET` | The GitHub App. |
| `GITHUB_ALLOWED_ACCOUNTS` | Logins whose installations sync. Anyone else's is logged and left alone. |
| `GITHUB_TOKEN`, `GITHUB_TOKEN_ACCOUNTS` | A personal token instead, for a local run. No webhooks. |
| `GITLAB_URL` | The GitLab instance, `https://gitlab.com`. |
| `GITLAB_CLIENT_ID`, `GITLAB_CLIENT_SECRET`, `GITLAB_REFRESH_TOKEN` | The OAuth application and the refresh token `redgreen setup gitlab` printed. |
| `GITLAB_ALLOWED_ACCOUNTS` | Group and user paths that sync. |
| `GITLAB_WEBHOOK_SECRET` | The secret token GitLab webhooks carry. |
| `GITLAB_TOKEN`, `GITLAB_TOKEN_ACCOUNTS` | An access token instead, and the namespaces it reads. |
| `SECRET_KEY` | Base64 of 32 bytes. Seals the GitLab tokens kept in the state. |
| `AZURE_DEVOPS_TENANT_ID`, `AZURE_DEVOPS_CLIENT_ID`, `AZURE_DEVOPS_CLIENT_SECRET` | An Entra application added to each organisation as a user. |
| `AZURE_DEVOPS_ORGANIZATIONS` | The organisations it reads, by name or URL. |

### Endpoints

- `GET /healthz` answers at once; it never waits on a sweep.
- `POST /api/webhooks/github`, checked by its HMAC signature.
- `POST /api/webhooks/gitlab`, checked by its secret token.

A webhook reads its repository again ahead of the sweep and writes it.
Azure DevOps sends none; the sweep covers it. GitHub's `workflow_job`
reads nothing again: it is measured as telemetry, below. An App made
before redgreen asked for that event needs it ticked under the App's
"Subscribe to events" (it needs no new permission).

### One-time setup

On your own machine:

```bash
PUBLIC_URL=https://redgreen.example.org redgreen setup github [--org <org>]
GITLAB_URL=... GITLAB_CLIENT_ID=... GITLAB_CLIENT_SECRET=... redgreen setup gitlab
```

Each opens a page on `127.0.0.1:8765`, sends you to the forge, takes the
answer back, and prints what goes into the secret store. `setup github
--from-store <old sqlite>` reads the GitHub App the old web app created,
with that app's `SECRET_KEY`. For GitLab, register the OAuth application
with the redirect URI `http://127.0.0.1:8765/callback` and the `read_api`
scope.

### Carrying history over

```bash
ORRERY_URL=... ORRERY_TOKEN=... ORRERY_READ_TOKEN=... redgreen import-history <old sqlite>
```

Reads the old web app's `deployments` table and writes each one with its
worked-out fields: forge deployments as `sw_deployment`, Argo CD syncs as
fields added to the entity the cluster pusher owns. It prints how many it
read, created, updated, left unchanged, and skipped, and how many the hub
rejected.

## Telemetry

JSON lines on stdout with `trace_id`. With OTLP on: a span per request, per
sweep, per repository read, and per outbound call, and these metrics:

- `redgreen.build_info` with `version`
- `redgreen.sync.duration` by provider and outcome
- `redgreen.quota.remaining` and `redgreen.quota.limit` by provider
- `redgreen.webhook.lag`, from a delivery to its repository written
- `redgreen.webhooks` by provider and outcome
- `redgreen.push.entities` by blueprint and outcome
- `redgreen.sweep.last_success`, the time of the last sweep that reached every forge
- `redgreen.orrery.describe` by outcome
- `redgreen.actions.jobs` (finished), `redgreen.actions.job.wait` (queued
  to a runner taking it), and `redgreen.actions.job.duration` (on the
  runner), by `repository`, `runner` (`hosted` or `self-hosted`), and `os`,
  the finished ones by `conclusion` too; and `redgreen.actions.jobs.active`,
  the jobs `queued` and `in_progress` now. From GitHub's `workflow_job`
  webhook, for tracked repositories. A job is hosted when GitHub's own runner
  group took it, or before one does, when it asks only for GitHub's images
  (`ubuntu-*`, `windows-*`, `macos-*`). A restart forgets the jobs in flight.
- `http.server.request.duration`, `process.cpu.time`, `process.memory.usage`

## Development

```bash
go vet ./...
go test ./...
go run ./scripts/blueprints > /tmp/blueprints.json
node scripts/check-expressions.mts /tmp/blueprints.json ../orrery
```

The last step compiles every calculation with Orrery's own expression code
and checks the health rules and the DORA fields on sample entities through
Orrery's `aggregate`. It needs an Orrery checkout with its dependencies
installed.

A local run against GitHub with a personal token:

```bash
GITHUB_TOKEN=$(gh auth token) GITHUB_TOKEN_ACCOUNTS=<owner> go run ./cmd/redgreen serve
```

The Orrery SDK is a private module. The image build fetches it through your
SSH agent, with a key that can read `dsswift/orrery`:

```bash
docker build --platform linux/amd64 --ssh default .
```
