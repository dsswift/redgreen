# redgreen

Estate health board. Read `README.md` first.

## Shape

- `src/shared` is the contract. The estate model in `model.ts` is provider
  neutral; the rules in `rules.ts` are the only place a "reason to not be
  green" is defined. Both are imported by the server and the web app.
- `src/server` runs directly under Node 24 with no build step. Imports carry
  the `.ts` extension. Only erasable TypeScript syntax is allowed: no enums,
  no parameter properties.
- `src/server/providers/<kind>` is one forge. A provider implements
  `Provider` in `providers/provider.ts` and nothing outside that directory may
  import forge-specific types. Adding a forge means adding a directory here
  and registering it in `main.ts`. What every forge shares (`isStatus`,
  `RateLimits`) lives directly under `providers/`.
- `server/orrery.ts` pushes every repo to an Orrery hub as a `repository`
  entity when `ORRERY_URL` and `ORRERY_TOKEN` are set. `toRepository` is the
  only place the mapping lives, and `REPOSITORY_BLUEPRINT` beside it is what
  the hub is offered: a field written must be defined there. `toDeployments`
  and `DEPLOYMENT_BLUEPRINT` do the same for the deployments a forge records.
  A provider that reads none leaves `deployments` off its snapshot. `toServices`
  and `SERVICE_BLUEPRINT` do the same for services. It speaks plain HTTP; do
  not add an SDK.
- `server/services.ts` finds the services in a repo from the files that build
  them. `detectServices` is the only place a service is recognised; a
  provider only hands over its file list and a reader through `files` on
  `Provider`. The answer depends on nothing but the files at one commit, and
  is read again only when the default branch moves or `SERVICE_RULES` does.
  Bump `SERVICE_RULES` with any change to a rule.
- Health is computed at read time from stored snapshots in
  `server/health/evaluate.ts`. Changing a rule never needs a resync.
- `src/web` is a Vite React app served by the server from `dist/web` in
  production and proxied from the Vite dev server locally. Every request goes
  through `call` in `api.ts`. When an answer says the sign-in has ended,
  `session.ts` reloads the page so the gate in front signs the person in
  again; a view never handles that itself.

## Rules of the house

- Every GET to a forge goes through that forge's conditional-request client
  (`providers/github/client.ts`, `providers/gitlab/client.ts`,
  `providers/azuredevops/client.ts`). Do not bypass it; a sweep must stay free
  when nothing changed. Azure DevOps sends an ETag on few resources, so there
  a sweep is kept cheap by asking for less: prefer one call that covers a
  project over one call per repo.
- Logs are JSON lines on stdout. Log both branches of any decision that
  changes what the board shows.
- No continuously repainting CSS animations in the web app.
- The GitHub App is public by design so it installs on several orgs. Unknown
  installations stay pending until approved in the UI. Keep that gate. GitLab
  groups found through OAuth go through the same gate. An Azure DevOps
  organization is named by a person in the settings or the environment, and
  naming it is the approval.
- GitLab access tokens expire in two hours. The provider refreshes them itself;
  never cache one outside `providers/gitlab/provider.ts`.
- Azure DevOps is read as an Entra service principal. Its tokens last about an
  hour and have no refresh token; the provider asks for the next one itself.
  Never cache one outside `providers/azuredevops/provider.ts`.
- A repository's language is one of `LANGUAGES` in `shared/languages.ts`,
  whatever the forge calls it. A provider that reads shares picks with
  `mainLanguage`; `toRepository` puts every other one through
  `listedLanguage`. Add a language to the list there, and to `NAMED` when a
  forge calls it something else.
- A service is pushed with the facts the files hold: its folder, its kind,
  whether it has its own README, Dockerfile, and tests (`serviceFiles` in
  `server/services.ts` is the only place that decides which service a file
  belongs to),
  its language, and its icon. What people decide about it, such as its tier,
  lifecycle, description, or runbook, is never pushed; a pushed value would
  fight their edits.
- The blueprints offered to the hub leave their fields open. Locking a field
  is the architect's choice, made in the hub.
- `package.json` is the version of record. The release tag must match it.

## Checks

```bash
npm run check
```

Run it before every commit.
