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
- open critical Dependabot and code scanning alerts
- failing checks on open pull requests

Each of these is a rule. Every rule has a severity you set: red, amber, info,
or off. Any repo can override a rule, mute a single pipeline, or mute itself.

It also shows counts and simple metrics: open issues and pull requests, stars,
last push, latest releases, success rate, and how long a repo has been red.

## Providers

GitHub is the provider today. The estate model is provider-neutral: an account,
a repo, a pipeline, a run, a release. GitLab and Azure DevOps are added by
writing a new provider behind the same interface. Nothing else changes.

GitHub is read through a GitHub App you create from the settings page in one
click. Webhooks refresh a repo within seconds of a run finishing; a sweep every
few minutes catches anything else. Every request is conditional, so a sweep of
an unchanged estate costs no API quota.

The app is public so it can be installed on several orgs. An installation from
an account you did not list waits for your approval in the UI.

## Run it

```bash
npm install
npm run dev        # server on :8080 with live reload, web on :5173
```

Then open http://localhost:5173/settings and create the GitHub App, or run
against a personal token for a quick look:

```bash
GITHUB_TOKEN=$(gh auth token) GITHUB_TOKEN_ACCOUNTS=my-org,my-user npm run dev:server
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
| `GITHUB_ALLOWED_ACCOUNTS` | | Logins that sync without approval |
| `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`, `GITHUB_APP_SLUG` | | App credentials from the environment, instead of the setup flow |
| `GITHUB_TOKEN`, `GITHUB_TOKEN_ACCOUNTS` | | Personal token mode for local runs |

There is no login of its own. Put it behind your own gate (oauth2-proxy, a VPN,
a LAN) and leave `POST /api/webhooks/github` and `GET /healthz` open.

## Deploy

```bash
docker build --platform linux/amd64 -t redgreen .
docker run -p 8080:8080 -v redgreen-data:/data -e PUBLIC_URL=https://redgreen.example.org redgreen
```

Pushing a tag `vX.Y.Z` that matches `package.json` publishes the image from CI.
One replica, one volume; the database is SQLite.

## Layout

```
src/shared     estate model, rules, wire types (server and web)
src/server     Hono API, SQLite store, sync scheduler, providers
src/web        React board
```

```bash
npm run check  # typecheck, tests, web build
```
