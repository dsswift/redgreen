import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { loadConfig } from './config.ts'
import { Estate } from './estate.ts'
import { Events } from './events.ts'
import { createApp } from './http.ts'
import { createLogger } from './log.ts'
import { Outcomes } from './outcomes/outcomes.ts'
import { AzureDevopsProvider } from './providers/azuredevops/provider.ts'
import { GithubProvider } from './providers/github/provider.ts'
import { GitlabProvider } from './providers/gitlab/provider.ts'
import { SecretBox } from './secrets.ts'
import { Store } from './store.ts'
import { Scheduler } from './sync/scheduler.ts'
import { createTelemetry } from './telemetry.ts'

function app() {
  const webDir = mkdtempSync(join(tmpdir(), 'redgreen-web-'))
  mkdirSync(join(webDir, 'assets'))
  writeFileSync(join(webDir, 'index.html'), '<!doctype html>')
  writeFileSync(join(webDir, 'favicon.svg'), '<svg/>')
  writeFileSync(join(webDir, 'assets', 'index-abc123.js'), 'export {}')

  const log = createLogger('test')
  const config = loadConfig({})
  const store = new Store(':memory:')
  const secrets = SecretBox.load(Buffer.alloc(32, 1).toString('base64'), webDir, log)
  const github = new GithubProvider(config, store, secrets, log)
  const gitlab = new GitlabProvider(config, store, secrets, log)
  const azuredevops = new AzureDevopsProvider(config, store, secrets, log)
  const providers = [github, gitlab, azuredevops]
  const estate = new Estate(store, providers)
  const events = new Events()
  const telemetry = createTelemetry(config, '0.0.0', log)
  const outcomes = new Outcomes(store, providers, telemetry, log)
  const scheduler = new Scheduler(providers, store, estate, events, outcomes, config, log)
  return createApp({ config, store, estate, scheduler, events, providers, outcomes, telemetry, github, gitlab, azuredevops, log, webDir })
}

describe('http', () => {
  // The site is served through a shared CDN. Anything a shared cache keeps
  // from a signed-in response is handed to the next visitor without sign-in.
  it.each(['/', '/repos/github%3A1', '/favicon.svg', '/api/estate', '/api/settings', '/healthz', '/assets/index-abc123.js'])(
    'forbids shared caches from storing %s',
    async (path) => {
      const response = await app().request(path)
      expect(response.status).toBe(200)
      expect(response.headers.get('cache-control')).toMatch(/\bprivate\b/)
    },
  )

  it('answers outcomes for a known window and refuses another', async () => {
    const ok = await app().request('/api/outcomes?window=7')
    expect(ok.status).toBe(200)
    expect(await ok.json()).toMatchObject({ window: 7, rows: [], sources: { forge: true, orrery: false, incidents: false } })
    expect((await app().request('/api/outcomes?window=14')).status).toBe(400)
    expect(((await (await app().request('/api/outcomes')).json()) as { window: number }).window).toBe(30)
  })

  it('accepts and drops a browser telemetry batch without a relay, and refuses one too large', async () => {
    const dropped = await app().request('/api/telemetry', { method: 'POST', body: JSON.stringify({ meta: {}, logs: [] }) })
    expect(dropped.status).toBe(204)
    const big = await app().request('/api/telemetry', { method: 'POST', body: 'x'.repeat(256 * 1024 + 1) })
    expect(big.status).toBe(413)
  })

  it('lets only the browser keep fingerprinted assets', async () => {
    const response = await app().request('/assets/index-abc123.js')
    expect(response.headers.get('cache-control')).toBe('private, max-age=31536000, immutable')
  })

  it('never stores pages or data', async () => {
    for (const path of ['/', '/api/estate', '/favicon.svg']) {
      const response = await app().request(path)
      expect(response.headers.get('cache-control')).toBe('private, no-store')
    }
  })
})
