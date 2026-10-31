import { serveStatic } from '@hono/node-server/serve-static'
import { readFileSync } from 'node:fs'
import { Hono } from 'hono'
import { streamSSE } from 'hono/streaming'
import { z } from 'zod'
import type { SettingsResponse } from '../shared/api.ts'
import { WINDOWS, type WindowDays } from '../shared/outcomes.ts'
import { RULE_KEYS, SEVERITIES, defaultRuleSeverities, type RuleSeverities } from '../shared/rules.ts'
import type { Config } from './config.ts'
import type { Estate } from './estate.ts'
import type { Events } from './events.ts'
import type { Logger } from './log.ts'
import type { Outcomes } from './outcomes/outcomes.ts'
import type { AzureDevopsProvider } from './providers/azuredevops/provider.ts'
import type { GithubProvider } from './providers/github/provider.ts'
import type { GitlabProvider } from './providers/gitlab/provider.ts'
import type { Provider } from './providers/provider.ts'
import type { Store } from './store.ts'
import type { Telemetry } from './telemetry.ts'
import type { Scheduler } from './sync/scheduler.ts'

export interface HttpDeps {
  config: Config
  store: Store
  estate: Estate
  scheduler: Scheduler
  events: Events
  providers: Provider[]
  outcomes: Outcomes
  telemetry: Telemetry
  github: GithubProvider
  gitlab: GitlabProvider
  azuredevops: AzureDevopsProvider
  log: Logger
  /** Directory holding the built web app, or null to serve the API only. */
  webDir: string | null
}

const severitySchema = z.enum(SEVERITIES)
const rulesSchema = z.object(Object.fromEntries(RULE_KEYS.map((k) => [k, severitySchema])) as Record<(typeof RULE_KEYS)[number], typeof severitySchema>)
const repoSettingsSchema = z.object({
  muted: z.boolean().optional(),
  mutedPipelines: z.array(z.string()).optional(),
  ruleOverrides: z.object(Object.fromEntries(RULE_KEYS.map((k) => [k, severitySchema.optional()]))).optional(),
})
const windowSchema = z.coerce.number().refine((n): n is WindowDays => (WINDOWS as readonly number[]).includes(n))
/** The largest browser telemetry batch relayed. */
const TELEMETRY_BATCH_LIMIT = 256 * 1024
const accountStatusSchema = z.object({ status: z.enum(['active', 'pending', 'ignored']) })
const gitlabAppSchema = z.object({ clientId: z.string().trim().min(1), clientSecret: z.string().trim().min(1) })
const azureDevopsAppSchema = z.object({
  tenantId: z.string().trim().min(1),
  clientId: z.string().trim().min(1),
  clientSecret: z.string().trim(),
  organizations: z.array(z.string().trim().min(1)).min(1),
})

export function createApp(deps: HttpDeps): Hono {
  const { config, store, estate, scheduler, events, providers, outcomes, telemetry, github, gitlab, azuredevops, log } = deps
  const app = new Hono()

  app.use('/api/*', async (c, next) => {
    const started = performance.now()
    await next()
    telemetry.request(c.req.routePath, c.req.method, c.res.status, (performance.now() - started) / 1000)
  })

  // Every response is private: the site sits behind a shared CDN, and a copy
  // kept there would reach the next visitor without sign-in. Fingerprinted
  // assets may still live in the browser's own cache.
  app.use('*', async (c, next) => {
    await next()
    const immutable = c.req.path.startsWith('/assets/') && c.res.status === 200
    c.res.headers.set('cache-control', immutable ? 'private, max-age=31536000, immutable' : 'private, no-store')
  })

  app.onError((error, c) => {
    log.error('request failed', { method: c.req.method, path: c.req.path, error })
    return c.json({ error: error.message }, 500)
  })

  app.get('/healthz', (c) => c.json({ ok: true }))

  // Estate ---------------------------------------------------------------------

  app.get('/api/estate', (c) => c.json(estate.estate(scheduler.currentStatus())))

  app.get('/api/repos/:id', (c) => {
    const detail = estate.detail(c.req.param('id'))
    return detail ? c.json(detail) : c.json({ error: 'Unknown repo' }, 404)
  })

  app.put('/api/repos/:id/settings', async (c) => {
    const id = c.req.param('id')
    if (!store.getRepo(id)) return c.json({ error: 'Unknown repo' }, 404)
    const patch = repoSettingsSchema.safeParse(await c.req.json())
    if (!patch.success) return c.json({ error: patch.error.message }, 400)
    const current = store.getRepoSettings(id)
    const next = {
      muted: patch.data.muted ?? current.muted,
      mutedPipelines: patch.data.mutedPipelines ?? current.mutedPipelines,
      ruleOverrides: patch.data.ruleOverrides
        ? Object.fromEntries(Object.entries(patch.data.ruleOverrides).filter(([, v]) => v !== undefined))
        : current.ruleOverrides,
    }
    store.setRepoSettings(id, next)
    log.info('repo settings changed', { repo: id, settings: next })
    const row = store.getRepo(id)
    if (row) estate.reconcileLevel(row)
    events.emit({ type: 'repo', id })
    return c.json(estate.detail(id))
  })

  app.post('/api/repos/:id/sync', async (c) => {
    const id = c.req.param('id')
    if (!store.getRepo(id)) return c.json({ error: 'Unknown repo' }, 404)
    await scheduler.syncNow(id)
    return c.json(estate.detail(id))
  })

  // Outcomes -------------------------------------------------------------------

  app.get('/api/outcomes', async (c) => {
    const window = windowSchema.safeParse(c.req.query('window') ?? 30)
    if (!window.success) return c.json({ error: `window must be one of ${WINDOWS.join(', ')}` }, 400)
    return c.json(await outcomes.compute(window.data))
  })

  // Browser telemetry ------------------------------------------------------------

  // The page posts here, never to the collector. The batch is capped, named, stripped of any user, and relayed with the key.
  app.post('/api/telemetry', async (c) => {
    const body = await c.req.text()
    if (body.length > TELEMETRY_BATCH_LIMIT) {
      telemetry.browserBatch('refused')
      return c.body(null, 413)
    }
    if (!config.FARO_URL || !config.FARO_API_KEY) {
      telemetry.browserBatch('dropped')
      return c.body(null, 204)
    }
    let batch: { meta?: Record<string, unknown> }
    try {
      batch = JSON.parse(body) as { meta?: Record<string, unknown> }
    } catch {
      telemetry.browserBatch('refused')
      return c.body(null, 400)
    }
    const { user: _user, ...kept } = batch.meta ?? {}
    const meta = { ...kept, app: { ...((batch.meta?.app as Record<string, unknown> | undefined) ?? {}), name: 'redgreen-web' } }
    try {
      const res = await fetch(config.FARO_URL, { method: 'POST', headers: { 'content-type': 'application/json', 'x-api-key': config.FARO_API_KEY }, body: JSON.stringify({ ...batch, meta }), signal: AbortSignal.timeout(10_000) })
      telemetry.browserBatch(res.ok ? 'relayed' : 'failed')
      if (!res.ok) log.warn('browser telemetry relay refused', { status: res.status })
    } catch (error) {
      telemetry.browserBatch('failed')
      log.warn('browser telemetry relay failed', { error })
    }
    return c.body(null, 204)
  })

  app.post('/api/sync', (c) => {
    void scheduler.sweep('manual')
    return c.json(scheduler.currentStatus(), 202)
  })

  // Settings -------------------------------------------------------------------

  const settingsResponse = (): SettingsResponse => ({
    rules: store.getRuleSeverities(),
    defaults: defaultRuleSeverities(),
    providers: providers.map((p) => p.status()),
    accounts: store.listAccounts().map(({ connection: _c, seenAt: _s, ...account }) => account),
    publicUrl: config.PUBLIC_URL,
    setup: {
      githubAppName: `redgreen-${new URL(config.PUBLIC_URL).hostname.split('.')[0]}`,
      gitlab: gitlab.setup(),
      azuredevops: azuredevops.setup(),
    },
  })

  app.get('/api/settings', (c) => c.json(settingsResponse()))

  app.put('/api/settings/rules', async (c) => {
    const parsed = rulesSchema.safeParse(await c.req.json())
    if (!parsed.success) return c.json({ error: parsed.error.message }, 400)
    const rules: RuleSeverities = parsed.data
    store.setRuleSeverities(rules)
    estate.reconcileAll()
    log.info('rule severities changed', { rules })
    events.emit({ type: 'estate' })
    return c.json(settingsResponse())
  })

  app.put('/api/accounts/:id', async (c) => {
    const id = c.req.param('id')
    if (!store.getAccount(id)) return c.json({ error: 'Unknown account' }, 404)
    const parsed = accountStatusSchema.safeParse(await c.req.json())
    if (!parsed.success) return c.json({ error: parsed.error.message }, 400)
    store.setAccountStatus(id, parsed.data.status)
    log.info('account status changed', { account: id, status: parsed.data.status })
    void scheduler.sweep('account status changed')
    return c.json(settingsResponse())
  })

  // GitHub setup and webhooks --------------------------------------------------

  app.get('/api/github/manifest', (c) => c.json(github.manifest(c.req.query('name') ?? settingsResponse().setup.githubAppName)))

  app.get('/setup/github/callback', async (c) => {
    const code = c.req.query('code')
    if (!code) return c.text('Missing code', 400)
    await github.completeSetup(code)
    void scheduler.sweep('github app created')
    return c.redirect('/settings?created=github')
  })

  // GitLab setup and webhooks --------------------------------------------------

  app.post('/api/gitlab/app', async (c) => {
    const parsed = gitlabAppSchema.safeParse(await c.req.json())
    if (!parsed.success) return c.json({ error: parsed.error.message }, 400)
    gitlab.saveApp(parsed.data.clientId, parsed.data.clientSecret)
    return c.json(settingsResponse())
  })

  app.get('/setup/gitlab/start', (c) => c.redirect(gitlab.startAuthorize()))

  app.get('/setup/gitlab/callback', async (c) => {
    const code = c.req.query('code')
    const state = c.req.query('state')
    if (!code || !state) return c.text(`GitLab did not send a code: ${c.req.query('error_description') ?? c.req.query('error') ?? 'unknown reason'}`, 400)
    try {
      await gitlab.completeAuthorize(code, state)
    } catch (error) {
      // A person lands here in a browser, so the reason has to be readable.
      log.warn('gitlab authorize failed', { error })
      return c.text(`GitLab authorization failed: ${error instanceof Error ? error.message : String(error)}`, 400)
    }
    void scheduler.sweep('gitlab authorized')
    return c.redirect('/settings?created=gitlab')
  })

  // Azure DevOps setup -----------------------------------------------------------

  app.post('/api/azuredevops/app', async (c) => {
    const parsed = azureDevopsAppSchema.safeParse(await c.req.json())
    if (!parsed.success) return c.json({ error: parsed.error.message }, 400)
    try {
      await azuredevops.saveApp(parsed.data)
    } catch (error) {
      // The reason is shown beside the form, so it has to be readable.
      log.warn('azure devops service principal rejected', { error })
      return c.json({ error: error instanceof Error ? error.message : String(error) }, 400)
    }
    void scheduler.sweep('azure devops connected')
    return c.json(settingsResponse())
  })

  // Webhooks -------------------------------------------------------------------

  for (const provider of [github, gitlab]) {
    app.post(`/api/webhooks/${provider.kind}`, async (c) => {
      const body = await c.req.text()
      let outcome
      try {
        outcome = await provider.handleWebhook(c.req.raw.headers, body)
      } catch (error) {
        log.warn('webhook rejected', { provider: provider.kind, error })
        return c.text('Rejected', 400)
      }
      for (const fullName of outcome.repos) scheduler.refreshByFullName(provider.kind, fullName, 'webhook')
      if (outcome.rediscover) void scheduler.sweep('webhook')
      return c.text('OK', 202)
    })
  }

  // Live updates -----------------------------------------------------------------

  app.get('/api/events', (c) =>
    streamSSE(c, async (stream) => {
      let n = 0
      const unsubscribe = events.subscribe((event) => void stream.writeSSE({ id: String(++n), event: event.type, data: JSON.stringify(event) }))
      stream.onAbort(unsubscribe)
      await stream.writeSSE({ event: 'sync', data: JSON.stringify({ type: 'sync', status: scheduler.currentStatus() }) })
      // Keep the connection open through idle proxies.
      while (!stream.aborted) {
        await stream.sleep(25_000)
        await stream.writeSSE({ event: 'ping', data: '' })
      }
    }),
  )

  // Web app --------------------------------------------------------------------

  if (deps.webDir) {
    const index = readFileSync(`${deps.webDir}/index.html`, 'utf8')
    app.use('/assets/*', serveStatic({ root: deps.webDir }))
    app.get('/favicon.svg', serveStatic({ root: deps.webDir, path: 'favicon.svg' }))
    app.get('*', (c) => c.html(index))
  }

  return app
}
