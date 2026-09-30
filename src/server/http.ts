import { serveStatic } from '@hono/node-server/serve-static'
import { readFileSync } from 'node:fs'
import { Hono } from 'hono'
import { streamSSE } from 'hono/streaming'
import { z } from 'zod'
import type { SettingsResponse } from '../shared/api.ts'
import { RULE_KEYS, SEVERITIES, defaultRuleSeverities, type RuleSeverities } from '../shared/rules.ts'
import type { Config } from './config.ts'
import type { Estate } from './estate.ts'
import type { Events } from './events.ts'
import type { Logger } from './log.ts'
import type { GithubProvider } from './providers/github/provider.ts'
import type { Provider } from './providers/provider.ts'
import type { Store } from './store.ts'
import type { Scheduler } from './sync/scheduler.ts'

export interface HttpDeps {
  config: Config
  store: Store
  estate: Estate
  scheduler: Scheduler
  events: Events
  providers: Provider[]
  github: GithubProvider
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
const accountStatusSchema = z.object({ status: z.enum(['active', 'pending', 'ignored']) })

export function createApp(deps: HttpDeps): Hono {
  const { config, store, estate, scheduler, events, providers, github, log } = deps
  const app = new Hono()

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
    githubAppName: `redgreen-${new URL(config.PUBLIC_URL).hostname.split('.')[0]}`,
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

  app.get('/api/github/manifest', (c) => c.json(github.manifest(c.req.query('name') ?? settingsResponse().githubAppName)))

  app.get('/setup/github/callback', async (c) => {
    const code = c.req.query('code')
    if (!code) return c.text('Missing code', 400)
    await github.completeSetup(code)
    void scheduler.sweep('github app created')
    return c.redirect('/settings?created=github')
  })

  app.post('/api/webhooks/github', async (c) => {
    if (!github.handleWebhook) return c.text('Not supported', 404)
    const body = await c.req.text()
    let outcome
    try {
      outcome = await github.handleWebhook(c.req.raw.headers, body)
    } catch (error) {
      log.warn('webhook rejected', { error })
      return c.text('Rejected', 400)
    }
    for (const fullName of outcome.repos) scheduler.refreshByFullName('github', fullName, 'webhook')
    if (outcome.rediscover) void scheduler.sweep('webhook')
    return c.text('OK', 202)
  })

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
