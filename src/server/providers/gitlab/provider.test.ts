import { describe, expect, it } from 'vitest'
import { loadConfig } from '../../config.ts'
import { createLogger } from '../../log.ts'
import { SecretBox } from '../../secrets.ts'
import { Store } from '../../store.ts'
import { GitlabProvider } from './provider.ts'

const log = createLogger('test')

function provider(env: Record<string, string>) {
  const config = loadConfig({ PUBLIC_URL: 'https://redgreen.example.org', ...env })
  const store = new Store(':memory:')
  return new GitlabProvider(config, store, SecretBox.load(Buffer.alloc(32, 1).toString('base64'), '/tmp', log), log)
}

describe('GitlabProvider', () => {
  it('is unconfigured without credentials and tells the settings page where to send GitLab', () => {
    const p = provider({})
    expect(p.configured()).toBe(false)
    expect(p.setup()).toEqual({
      url: 'https://gitlab.com',
      redirectUri: 'https://redgreen.example.org/setup/gitlab/callback',
      app: null,
      connectedAs: null,
      webhookUrl: 'https://redgreen.example.org/api/webhooks/gitlab',
      webhookSecret: null,
    })
  })

  it('holds an application from the environment but stays unconfigured until authorized', () => {
    const p = provider({ GITLAB_URL: 'https://git.example.org/', GITLAB_CLIENT_ID: 'abc', GITLAB_CLIENT_SECRET: 'shh', GITLAB_TOKEN: 'glpat-x' })
    expect(p.configured()).toBe(false)
    expect(p.setup().app).toEqual({ source: 'environment', clientId: 'abc' })
    const url = new URL(p.startAuthorize())
    expect(url.origin + url.pathname).toBe('https://git.example.org/oauth/authorize')
    expect(url.searchParams.get('client_id')).toBe('abc')
    expect(url.searchParams.get('redirect_uri')).toBe('https://redgreen.example.org/setup/gitlab/callback')
    expect(url.searchParams.get('scope')).toBe('read_api')
    expect(url.searchParams.get('state')).toHaveLength(32)
  })

  it('refuses an application from the settings page when the environment provides one', () => {
    const p = provider({ GITLAB_CLIENT_ID: 'abc', GITLAB_CLIENT_SECRET: 'shh' })
    expect(() => p.saveApp('other', 'secret')).toThrow(/environment/)
  })

  it('rejects an authorize callback whose state it did not issue', async () => {
    const p = provider({ GITLAB_CLIENT_ID: 'abc', GITLAB_CLIENT_SECRET: 'shh' })
    p.startAuthorize()
    await expect(p.completeAuthorize('code', 'not-the-state')).rejects.toThrow(/did not start here/)
  })

  it('is configured by an access token', () => {
    const p = provider({ GITLAB_TOKEN: 'glpat-x', GITLAB_TOKEN_ACCOUNTS: 'acme,someone' })
    expect(p.status()).toMatchObject({ kind: 'gitlab', configured: true, source: 'access token' })
  })

  describe('webhooks', () => {
    const p = provider({ GITLAB_TOKEN: 'glpat-x', GITLAB_WEBHOOK_SECRET: 's3cret' })
    const deliver = (event: string, payload: unknown, token = 's3cret') =>
      p.handleWebhook(new Headers({ 'x-gitlab-event': event, 'x-gitlab-token': token }), JSON.stringify(payload))

    it('asks for the named project to be refreshed', async () => {
      const outcome = await deliver('Pipeline Hook', { object_kind: 'pipeline', project: { path_with_namespace: 'acme/platform/api' } })
      expect(outcome).toEqual({ repos: ['acme/platform/api'], rediscover: false })
    })

    it('rejects a bad token', async () => {
      await expect(deliver('Push Hook', { project: { path_with_namespace: 'acme/api' } }, 'wrong')).rejects.toThrow(/token/)
    })
  })
})
