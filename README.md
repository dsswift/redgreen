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
| `sw_service` | inventory | each service found from the files that build it (folder, kind, language, icon, last human change, README, Dockerfile, tests), and its DORA measures. The one a repository's `orrery.yaml` describes also carries its logo, the APIs it calls (`consumes`, `will_consume`), and the fields and relations that place it in the estate's scheme. |
| `sw_api` | inventory | each API a repository's `orrery.yaml` says its service provides: kind, description, and where its definition is read |
| `sw_deployment` | history, kept 180 days | each deployment a forge recorded, with lead time, commits carried, fix only, failed change, and restore time. Argo CD syncs another source writes get the same fields added, and so does a forge job another source already reports by its address: that source's entity is the deployment, and redgreen keeps no copy of its own. |
| `sw_issue` | record, kept 90 days after closing | open issues and pull requests on every forge, linked to their repository |
| `sw_connection` | inventory | each host a service's config files name, per environment: host, resource name, database, kind, and the setting names, with evidence `code`. Names only, never a value. |
| `team` | fields only | the same DORA measures over a team's services, through the standard's `sw_service.team` |

### What a repository says about itself

A repository describes the service it builds in an `orrery.yaml`: at its
root, or beside the service in a repository that builds several. It holds
the service's key, its logo, the APIs it provides and calls, and, for one
estate, its place in that estate's scheme; paths in it are from the
repository root. redgreen reads the files with the services, at the same
commit, so an estate lists exactly the services of the repositories it
reads, and nothing it does not. The key must be the repository's own, or a
folder in it; a file in a folder names a service at or under that folder. The place (`fields` and `relations`) is the estate's own
words: it travels on the service in every write, push and sweep alike,
because each write replaces what redgreen wrote before. Where the hub lacks
one of its names, the service is written again without its place and stands
without it. A file that cannot be used is logged and left
out; the services still stand.

### What a service's config files say it reaches

redgreen reads the config files in each service's own folder, at the same
commit as the services: `appsettings*.json`, `local.settings.json`,
`web*.config` with its transforms (`Web.Prod.config`),
`ApplicationInsights.config`, and `*.env.example`. It reads connection
strings, URLs, Key Vault references, Redis configuration strings, and Azure
host names, and keeps the host, the database, and the names of the settings
that named them. Every value, password, and key is dropped while the file
is read. An App Insights instrumentation key is kept, as `app_insights_key`
on a connection of kind `appinsights`: it is an identifier, not a secret.

The environment is the word in the file name: Production or Prod, Staging,
Test, Dev, Preprod, Uat, or Qa, written as `Prod`, `Staging`, `Test`, `Dev`,
`Preprod`, `Uat`, or `Qa`. A file that names none, such as `appsettings.json`
or `Web.config`, is `unknown`. Each record is keyed
`<service key>@<environment>><host>[/<database>]@code` and linked to its
service; the estate's hub links the environment, the app, and the resource
by its own rules. redgreen offers `sw_connection` with only the fields it
writes, each as an estate defines it, so a hub that already has the
blueprint gains nothing from the offer. A config file that cannot be parsed
is logged and left out.

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
| `flaky`: one commit both passed and failed in the recent runs, and too few runs have passed since | pipeline | amber |
| `critical_alerts`: open critical dependency or code scanning alerts | repository | red |
| `pr_checks_failing`: an open, ready pull request has failing checks | repository | red |
| `archive_candidate`: idle 90 days or more, and red | repository | amber |

`flaky` counts runs, not days, so it follows how often a pipeline runs. It
reads the newest 20 finished runs of any branch. The evidence is a run whose
result differs from the run before it on the same commit. The flag clears
once the newest 5 runs have all passed with no such evidence after the first
of them, and `status_reason` says how many more green runs that takes
(`flaky_clears_in_runs`). An hourly pipeline clears in 5 hours and a nightly
one in 5 nights. Evidence older than 180 days is dropped, so a pipeline that
runs a few times a year is not held amber by one bad day for years; without
that bound it would wait more than a year for 5 runs. `FLAKY_WINDOW_RUNS`,
`FLAKY_CLEAR_RUNS`, and `FLAKY_MAX_AGE_DAYS` change the three numbers. A
forge lists at most 30 runs of a pipeline and redgreen keeps runs for 60
days, so a wider window reaches no further for a pipeline that runs seldom.

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

A deployment is what a forge records against an environment. On GitHub and
GitLab that is a deployment. On Azure DevOps it is each job a pipeline ran
against an environment (Pipelines, Environments), with the commit of the
build it ran in; a build the forge no longer keeps takes its deployments
with it. Azure DevOps shows an environment's deployments only to an identity
with the Reader role on it. Give the application that role on each project's
environments (Pipelines, Environments, Security), or the project's
deployments are not read.

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
| `FLAKY_WINDOW_RUNS`, `FLAKY_CLEAR_RUNS`, `FLAKY_MAX_AGE_DAYS` | The flaky rule: finished runs read, 20; passes in a row that clear it, 5; days evidence counts, 180. |
| `ORRERY_URL` | The hub's machine surface. |
| `ORRERY_TOKEN` | A source token, registered for the blueprints above, with a delete grant on `sw_deployment` so it can remove its copy of a job another source reports. |
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
Azure DevOps sends none; the sweep covers it, and reads GitLab's and Azure
DevOps's CI jobs too. GitHub's `workflow_job` reads nothing again: it is measured as telemetry, below. An App made
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
- `redgreen.actions.jobs` (finished), `redgreen.actions.job.wait` (queued
  to a runner taking it), and `redgreen.actions.job.duration` (on the
  runner), by `repository`, `runner` (`hosted` or `self-hosted`), and `os`,
  the finished ones by `conclusion` too; and `redgreen.actions.jobs.active`,
  the jobs `queued` and `in_progress` now, for tracked repositories.
  - GitHub: from the `workflow_job` webhook. A job is hosted when GitHub's
    own runner group took it, or before one does, when it asks only for
    GitHub's images (`ubuntu-*`, `windows-*`, `macos-*`).
  - GitLab: each sync reads the project's 50 newest jobs. The wait is
    GitLab's `queued_duration`; a job waiting on an earlier stage, a person,
    or a schedule is left out until it waits for a runner. Only gitlab.com's
    shared runners are hosted; on a self-managed instance every runner is
    the estate's.
  - Azure DevOps: each sync reads the timelines of the builds queued in the
    last three hours, a finished one once. A job is hosted when a Microsoft
    agent (`Hosted Agent`, `Azure Pipelines n`) took it. Only a build's first
    job has a known wait, from the build's queue time; the OS is `unknown`.
  Polled states are only as fresh as the sweep. A restart forgets the jobs
  in flight and never counts a job that had started before it again.
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
