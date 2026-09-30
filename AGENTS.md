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
  import forge-specific types. Adding GitLab or Azure DevOps means adding a
  directory here and registering it in `main.ts`.
- Health is computed at read time from stored snapshots in
  `server/health/evaluate.ts`. Changing a rule never needs a resync.
- `src/web` is a Vite React app served by the server from `dist/web` in
  production and proxied from the Vite dev server locally.

## Rules of the house

- Every GET to a forge goes through the conditional-request hook in
  `providers/github/client.ts`. Do not bypass it; a sweep must stay free when
  nothing changed.
- Logs are JSON lines on stdout. Log both branches of any decision that
  changes what the board shows.
- No continuously repainting CSS animations in the web app.
- The GitHub App is public by design so it installs on several orgs. Unknown
  installations stay pending until approved in the UI. Keep that gate.
- `package.json` is the version of record. The release tag must match it.

## Checks

```bash
npm run check
```

Run it before every commit.
