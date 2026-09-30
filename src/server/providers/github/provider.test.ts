import { createHmac, generateKeyPairSync } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { loadConfig } from '../../config.ts'
import { createLogger } from '../../log.ts'
import { SecretBox } from '../../secrets.ts'
import { Store } from '../../store.ts'
import { GithubProvider } from './provider.ts'

const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
const pem = privateKey.export({ type: 'pkcs1', format: 'pem' }).toString()
const log = createLogger('test')

function provider(env: Record<string, string>) {
  const config = loadConfig({ PUBLIC_URL: 'https://redgreen.example.org', ...env })
  const store = new Store(':memory:')
  return new GithubProvider(config, store, SecretBox.load(Buffer.alloc(32, 1).toString('base64'), '/tmp', log), log)
}

describe('GithubProvider', () => {
  it('is unconfigured without credentials and exposes a manifest for the setup flow', () => {
    const p = provider({})
    expect(p.configured()).toBe(false)
    const { action, manifest } = p.manifest('redgreen-test')
    expect(action).toBe('https://github.com/settings/apps/new')
    expect(manifest).toMatchObject({
      name: 'redgreen-test',
      public: true,
      redirect_url: 'https://redgreen.example.org/setup/github/callback',
      hook_attributes: { url: 'https://redgreen.example.org/api/webhooks/github', active: true },
    })
  })

  it('prefers app credentials from the environment', () => {
    const p = provider({ GITHUB_APP_ID: '42', GITHUB_APP_PRIVATE_KEY: pem, GITHUB_APP_SLUG: 'redgreen-test', GITHUB_TOKEN: 'ghp_x' })
    expect(p.status()).toMatchObject({ configured: true, source: 'GitHub App (environment)' })
    expect(p.status().links[0]?.url).toBe('https://github.com/apps/redgreen-test/installations/new')
  })

  describe('webhooks', () => {
    const p = provider({ GITHUB_APP_ID: '42', GITHUB_APP_PRIVATE_KEY: pem, GITHUB_APP_WEBHOOK_SECRET: 's3cret' })
    const deliver = (event: string, payload: unknown, secret = 's3cret') => {
      const body = JSON.stringify(payload)
      const signature = 'sha256=' + createHmac('sha256', secret).update(body).digest('hex')
      const headers = new Headers({ 'x-github-event': event, 'x-github-delivery': 'd1', 'x-hub-signature-256': signature })
      return p.handleWebhook(headers, body)
    }

    it('asks for the named repo to be refreshed', async () => {
      const outcome = await deliver('workflow_run', { action: 'completed', repository: { full_name: 'acme/repo' } })
      expect(outcome).toEqual({ repos: ['acme/repo'], rediscover: false })
    })

    it('asks for rediscovery on installation changes', async () => {
      const outcome = await deliver('installation_repositories', { action: 'added', installation: { id: 1 } })
      expect(outcome).toEqual({ repos: [], rediscover: true })
    })

    it('rejects a bad signature', async () => {
      await expect(deliver('push', { repository: { full_name: 'acme/repo' } }, 'wrong')).rejects.toThrow()
    })
  })
})
