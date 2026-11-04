# redgreen

One board for the health of every repository you own, across every forge.

Open it and see the whole estate: what exists, what is green, and what is on
fire. Red means something that should pass is failing. Amber means look at it.
Green means it passes. Repos that run nothing sit in a quiet list and never
turn the board red.

## What it watches

Per repo, per pipeline:

- the latest run on the default branch
- scheduled pipelines that stopped firing, or that the forge switched off
- the build of the newest release or tag
- open critical dependency and code scanning alerts
- failing checks on open pull requests

Each of these is a rule. Every rule has a severity you set: red, amber, info,
or off. Any repo can override a rule, mute a single pipeline, or mute itself.

It also shows counts and simple metrics: open issues and pull requests, stars,
last push, latest releases, success rate, and how long a repo has been red.

## Outcomes

`/outcomes` shows the four DORA measures for the estate and for every service
that deploys, over 7, 30, or 90 days, each set against the window before it.

| Measure | How it is counted today | What it still lacks |
| --- | --- | --- |
| Deployment frequency | Successful deployments per day. A deployment is one GitHub or GitLab recorded against an environment, or an ArgoCD sync Orrery holds (`ORRERY_READ_TOKEN`). Azure DevOps records none. | Deployments from any other tool |
| Lead time for changes | Median and p90 of the hours from a commit's authoring to the end of the deployment that carried it. The commits carried are those between the previous successful deployment's revision and this one's, read once through the forge's compare endpoint and kept. Where the sync's revision is a manifest in another repository, the service's own release tags stand in. | Commits on Azure DevOps; a path filter for a monorepo service |
| Change failure rate | The share of deployments that failed, were followed within a day by a deployment carrying only `fix` or `revert` commits, or were followed within a day by a critical or warning incident on the same service in Dispatch (`DISPATCH_URL`). | Incidents from any other desk |
| Time to restore | Median hours from a failed deployment to the next successful one, or to the end of the incident that failed it: when Dispatch marks it mitigated, or resolved when it skipped that step. | The same |

Each measure has a DORA band, elite to low, and a service's grade is the worst
of its bands. Deployments are kept for 200 days and the numbers are computed
when the page is read, so a change to a rule never needs a resync.

## Providers

GitHub, GitLab, and Azure DevOps are the providers today. The estate model is
provider-neutral: an account, a repo, a pipeline, a run, a release. Another
forge is added by writing a new provider behind the same interface. Nothing
else changes.

GitHub is read through a GitHub App you create from the settings page in one
click. Webhooks refresh a repo within seconds of a run finishing; a sweep every
few minutes catches anything else. Every request is conditional, so a sweep of
an unchanged estate costs no API quota.

The app is public so it can be installed on several orgs. An installation from
an account you did not list waits for your approval in the UI.

GitLab is read through an OAuth application you create on GitLab (user or
group settings, Applications) with the `read_api` scope and the redirect URI
the settings page shows. Paste its id and secret, authorize in the browser, and
every group you belong to appears as a pending account; switch on the ones you
want. A group brings all of its projects, subgroups included. gitlab.com and
self-managed both work; set `GITLAB_URL` for the latter.

GitLab has no app-level webhooks, so polling does the work. For faster
refreshes, add a webhook on a group or project with the URL and secret token
the settings page shows, on the pipeline, push, tag push, merge request, and
release events.

A GitLab project shows as one pipeline plus one per pipeline schedule.
Vulnerability counts need GitLab Ultimate; on other tiers the security feeds
read "not available".

Azure DevOps is read as a service principal: an application you register in
Microsoft Entra, with a client secret. The organization must be connected to
that Entra tenant. Add the application to the organization as a user with Basic
access, and to the Readers group of each project you want on the board. Then
enter its tenant, id, secret, and the organization names on the settings page,
or set them in the environment. An organization you name shows at once; there
is nothing to approve, since naming it is the approval.

An organization is one account. Each repository shows one pipeline per build
definition that builds it. A schedule is watched for missed runs only when it
fires whether or not the branch changed: `always: true` in YAML, or "only
schedule builds if the source or pipeline has changed" switched off in the
editor. A pull request's checks are the builds of its merge ref. Alert counts
need GitHub Advanced Security for Azure DevOps on the repository; without it
the security feeds read "not available". A disabled repository shows as
archived.

Azure DevOps has no releases, stars, or per-repository issues, so those read
empty. It sends an ETag on few resources, so a sweep is answered in full and
counts against the organization's throttle. There are no webhooks yet; the
sweep does the work.

## Run it

```bash
npm install
npm run dev        # server on :8080 with live reload, web on :5173
```

Then open http://localhost:5173/settings and create the GitHub App, or run
against a personal token for a quick look:

```bash
GITHUB_TOKEN=$(gh auth token) GITHUB_TOKEN_ACCOUNTS=my-org,my-user npm run dev:server
GITLAB_TOKEN=glpat-... GITLAB_TOKEN_ACCOUNTS=my-group,my-user npm run dev:server
```

Configuration is by environment variable:

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8080` | Listen port |
| `DATA_DIR` | `./data` | SQLite database and generated secret key |
| `PUBLIC_URL` | `http://localhost:8080` | URL browsers and GitHub reach the app on; drives the app manifest and webhook URL |
| `SECRET_KEY` | generated | Base64 32-byte key that encrypts provider credentials at rest |
| `SYNC_INTERVAL_MINUTES` | `5` | Minutes between full sweeps |
| `SYNC_CONCURRENCY` | `4` | Repos synced at once |
| `ORRERY_READ_TOKEN` | | An access token for the same hub, which may read where a source token may not. With it, every ArgoCD sync the hub holds as an `sw_deployment` is read for Outcomes and kept against the repository the synced workload's `sw_workload` catalog entry names, with the folder when that entry points into a monorepo |
| `DISPATCH_URL`, `DISPATCH_TOKEN` | | A Dispatch desk's API port (such as `http://dispatch.dispatch.svc:8081`) and a token it accepts: one issued under Settings, API tokens on the desk, or its shared `ALERTMANAGER_TOKEN`. With both, Outcomes reads `GET /v1/incidents` (at most once a minute, ten seconds at most) and counts each critical or warning incident against the repository and folder its Orrery workload's catalog entry names, falling back to the incident's service as the workload key. Tying an incident to a repository needs the catalog, so `ORRERY_READ_TOKEN` too. A failed read is logged and Outcomes counts deployments alone |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` | | Where traces and metrics go over OTLP/HTTP, and the service name (`redgreen`). Unset, telemetry is off. The server sends `redgreen.build_info`, `http.server.request.duration`, a `redgreen.outcomes.compute` span with its duration, `redgreen.outcomes.deployments_ingested` by source and outcome, `redgreen.outcomes.incidents_read` by source and outcome (`ok`, `failed`), and `redgreen.orrery.describe` by outcome (`ok`, `failed`) |
| `FARO_URL`, `FARO_API_KEY` | | Where the page's telemetry is relayed. The page posts to `/api/telemetry`; the server caps a batch at 256 KB, names the app `redgreen-web`, strips any user, and relays with the key. Without the key batches are accepted and dropped |
| `ORRERY_URL`, `ORRERY_TOKEN` | | An Orrery hub's ingest URL and a source token. When both are set, every repo is pushed there as an `sw_repository` entity, with its main language picked from one fixed list: language, branch protection, last push, pipeline health, alert counts, and a link back to its page here. Every service found in a repo is pushed as an `sw_service` entity homed in it: each app, API, job, command-line tool, library, or infrastructure root, found from the files that build it, with its folder, its kind, its own language and icon, who last changed it and who last changed it by hand, each with the name and the address on the commit so the hub can link the change to a person, and whether it has its own README, Dockerfile, and tests, so a monorepo shows each service as documented, containerized, and tested on its own. A service's icon comes from its repository: when the root `orrery.yaml` names an `icon: {name, file}`, the service whose key is that file's `service` wears that name, and the file (`.svg`, `.png`, or `.webp`, 256 KiB at most, the name matching `^[a-z0-9][a-z0-9_]{0,62}$`) is put on the hub first as a custom icon with `PUT /api/icons/{name}`, again only when its bytes change. A failed put is logged and that service is written without an icon, so the hub keeps the one it has. Every other service wears the icon of its language. Repositories and services also carry their last 30 days of Outcomes as `dora_deploy_frequency_30d` (per day), `dora_lead_time_p50_hours`, `dora_change_failure_rate_30d` (%), `dora_time_to_restore_p50_hours`, and `dora_grade` (elite, high, medium, low). Every finished deployment GitHub or GitLab recorded against an environment is pushed as an `sw_deployment` entity, so the hub can count deployments per week and how many failed. The blueprints are offered to the hub first, each with the id it had before the blueprint standard under `formerly`, so a hub that still has `repository`, `service`, or `deployment` renames it rather than making a second. The source must be registered for `sw_repository`, for `sw_service` to have services pushed, and for `sw_deployment` to have deployments pushed |
| `ORRERY_PUSH_TOKEN` | | A source token for the same hub as `ORRERY_URL`. When both are set, redgreen describes itself there from `orrery.yaml` at the repository root (the image carries it): it puts its logo (`icon: {name, file}`, here `logo.svg`, which the image also carries) on the hub with `PUT /api/icons/{name}`, then patches its own `sw_service` with that `icon`, `origin`, the `fields` and `relations` the file sets to place it in the estate's own scheme (here a `category` and its `pillars`), and the APIs it `consumes` and `will_consume`, and puts an `sw_api` for each API it provides. The blueprints it needs are offered to the hub first, with `formerly` naming `service` and `api`, so the token's source must be registered for `sw_service` and `sw_api`. It writes at start, again after a failure (30 seconds, 1, 5, then 15 minutes apart), and every six hours, counted as `redgreen.orrery.describe` by outcome. It reads only this token, never `ORRERY_TOKEN`; one secret may set both |
| `BOT_AUTHORS` | | Extra commit author names or emails, comma separated, to treat as bots when finding a service's last human change. Names ending in `[bot]`, addresses on the `.invalid` domain, and common release and dependency tools are already skipped. |
| `GITHUB_ALLOWED_ACCOUNTS` | | Logins that sync without approval |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`, `GITHUB_APP_SLUG` | | App credentials from the environment, instead of the setup flow |
| `GITHUB_TOKEN`, `GITHUB_TOKEN_ACCOUNTS` | | Personal token mode for local runs |
| `GITLAB_URL` | `https://gitlab.com` | The GitLab instance |
| `GITLAB_ALLOWED_ACCOUNTS` | | Group or user paths that sync without approval |
| `GITLAB_CLIENT_ID`, `GITLAB_CLIENT_SECRET` | | OAuth application from the environment, instead of the settings page; authorizing still happens in the browser |
| `GITLAB_WEBHOOK_SECRET` | generated | Secret token expected on GitLab webhooks |
| `GITLAB_TOKEN`, `GITLAB_TOKEN_ACCOUNTS` | | Access token mode for local runs |
| `AZURE_DEVOPS_TENANT_ID`, `AZURE_DEVOPS_CLIENT_ID`, `AZURE_DEVOPS_CLIENT_SECRET` | | Service principal from the environment, instead of the settings page |
| `AZURE_DEVOPS_ORGANIZATIONS` | | Organization names the service principal reads; required with the three above |

There is no login of its own. Put it behind your own gate (oauth2-proxy, a VPN,
a LAN) and leave `POST /api/webhooks/github`, `POST /api/webhooks/gitlab`, and
`GET /healthz` open. `POST /api/telemetry` is called by the page, so it sits
behind the gate with it.

When a sign-in ends while a page is open, the page reloads itself and the gate
signs the person in again, back where they were. The web app reads that from a
redirect or a `401` on any `/api` request. A gate that renews its session
quietly keeps an open tab from getting that far.

## Deploy

```bash
docker build --platform linux/amd64 -t redgreen .
docker run -p 8080:8080 -v redgreen-data:/data -e PUBLIC_URL=https://redgreen.example.org redgreen
```

Each release CI cuts on `main` publishes the image, tagged with the version, to
the public GitHub Container Registry as `ghcr.io/dsswift/redgreen`. A cluster pulls that with no secret; pin it by
digest. The image carries the server's TypeScript source, as it runs, without
its tests.
One replica, one volume; the database is SQLite.

## Layout

```
src/shared     estate model, rules, outcomes, wire types (server and web)
src/server     Hono API, SQLite store, sync scheduler, providers
src/web        React board
```

```bash
npm run check  # typecheck, tests, web build
```
