import { App, Octokit } from 'octokit'
import type { RepoSnapshot } from '../../../shared/model.ts'
import type { Config } from '../../config.ts'
import type { Logger } from '../../log.ts'
import type { SecretBox } from '../../secrets.ts'
import type { AccountRow, DiscoveredRepo, Store } from '../../store.ts'
import type { DiscoveredAccount, Provider, ProviderStatus, WebhookOutcome } from '../provider.ts'
import { RateLimits } from '../rate-limits.ts'
import { withConditionalRequests } from './client.ts'
import type { GhRepo } from './normalize.ts'
import { syncGithubRepo } from './repo-sync.ts'

const APP_SETTING = 'github.app'

/** A GitHub App created through the setup flow, stored encrypted. */
interface StoredApp {
  appId: number
  slug: string
  htmlUrl: string
  privateKey: string
  webhookSecret: string
}

type Credentials =
  | { mode: 'app'; source: 'environment' | 'setup'; appId: number; slug: string | null; htmlUrl: string | null; privateKey: string; webhookSecret: string | null }
  | { mode: 'token'; token: string; accounts: string[] }

interface GhAccount {
  id: number
  login: string
  name?: string | null
  type: string
  avatar_url: string
  html_url: string
}

export class GithubProvider implements Provider {
  readonly kind = 'github' as const
  private credentials: Credentials | null = null
  private app: App | null = null
  private readonly limits = new RateLimits()

  private readonly config: Config
  private readonly store: Store
  private readonly secrets: SecretBox
  private readonly log: Logger

  constructor(config: Config, store: Store, secrets: SecretBox, log: Logger) {
    this.config = config
    this.store = store
    this.secrets = secrets
    this.log = log
    this.reload()
  }

  /** Re-reads credentials. Environment wins over the setup flow, which wins over a token. */
  reload(): void {
    const { config } = this
    this.app = null
    if (config.GITHUB_APP_ID && config.GITHUB_APP_PRIVATE_KEY) {
      this.credentials = {
        mode: 'app',
        source: 'environment',
        appId: Number(config.GITHUB_APP_ID),
        slug: config.GITHUB_APP_SLUG ?? null,
        htmlUrl: config.GITHUB_APP_SLUG ? `https://github.com/apps/${config.GITHUB_APP_SLUG}` : null,
        privateKey: config.GITHUB_APP_PRIVATE_KEY.replace(/\\n/g, '\n'),
        webhookSecret: config.GITHUB_APP_WEBHOOK_SECRET ?? null,
      }
      this.log.info('github credentials: app from environment', { appId: this.credentials.appId })
      return
    }
    const stored = this.store.getSetting<StoredApp>(APP_SETTING)
    if (stored) {
      this.credentials = {
        mode: 'app',
        source: 'setup',
        appId: stored.appId,
        slug: stored.slug,
        htmlUrl: stored.htmlUrl,
        privateKey: this.secrets.open(stored.privateKey),
        webhookSecret: this.secrets.open(stored.webhookSecret),
      }
      this.log.info('github credentials: app from setup flow', { appId: stored.appId, slug: stored.slug })
      return
    }
    if (config.GITHUB_TOKEN) {
      this.credentials = { mode: 'token', token: config.GITHUB_TOKEN, accounts: config.GITHUB_TOKEN_ACCOUNTS }
      this.log.info('github credentials: personal token', { accounts: config.GITHUB_TOKEN_ACCOUNTS })
      return
    }
    this.credentials = null
    this.log.warn('github credentials: none; waiting for setup')
  }

  configured(): boolean {
    return this.credentials !== null
  }

  status(): ProviderStatus {
    const c = this.credentials
    const links: ProviderStatus['links'] = []
    if (c?.mode === 'app' && c.slug) {
      links.push({ label: 'Install on an account', url: `https://github.com/apps/${c.slug}/installations/new` })
      links.push({ label: 'App settings', url: c.htmlUrl ?? `https://github.com/apps/${c.slug}` })
    }
    return {
      kind: 'github',
      configured: c !== null,
      source: c === null ? null : c.mode === 'token' ? 'personal token' : `GitHub App (${c.source})`,
      links,
      rateLimits: this.limits.list(),
    }
  }

  // Setup flow -----------------------------------------------------------------

  /** The manifest a browser posts to GitHub to create the app in one step. */
  manifest(name: string): { action: string; manifest: Record<string, unknown> } {
    const publicUrl = this.config.PUBLIC_URL.replace(/\/$/, '')
    const reachable = publicUrl.startsWith('https://')
    return {
      action: 'https://github.com/settings/apps/new',
      manifest: {
        name,
        url: publicUrl,
        description: 'Estate health board. Read-only.',
        public: true,
        redirect_url: `${publicUrl}/setup/github/callback`,
        hook_attributes: { url: `${publicUrl}/api/webhooks/github`, active: reachable },
        default_permissions: {
          actions: 'read',
          administration: 'read',
          checks: 'read',
          contents: 'read',
          issues: 'read',
          metadata: 'read',
          pull_requests: 'read',
          statuses: 'read',
          vulnerability_alerts: 'read',
          security_events: 'read',
        },
        default_events: [
          'check_suite',
          'code_scanning_alert',
          'create',
          'delete',
          'dependabot_alert',
          'pull_request',
          'push',
          'release',
          'repository',
          'workflow_run',
        ],
      },
    }
  }

  /** Finishes the manifest flow: swaps the one-time code for the app's credentials. */
  async completeSetup(code: string): Promise<void> {
    const { data } = await new Octokit().request('POST /app-manifests/{code}/conversions', { code })
    const created = data as { id: number; slug: string; html_url: string; pem: string; webhook_secret: string | null }
    const stored: StoredApp = {
      appId: created.id,
      slug: created.slug,
      htmlUrl: created.html_url,
      privateKey: this.secrets.seal(created.pem),
      webhookSecret: this.secrets.seal(created.webhook_secret ?? ''),
    }
    this.store.setSetting(APP_SETTING, stored)
    this.log.info('github app created through setup flow', { appId: created.id, slug: created.slug })
    this.reload()
  }

  // Discovery -------------------------------------------------------------------

  async discoverAccounts(): Promise<DiscoveredAccount[]> {
    const c = this.credentials
    if (!c) return []
    if (c.mode === 'token') {
      const kit = this.tokenClient('token')
      const { data: me } = await kit.request('GET /user')
      const accounts = await Promise.all(
        c.accounts.map(async (login) => {
          const { data } = await kit.request('GET /users/{username}', { username: login })
          return toAccount(data as GhAccount, { type: 'github-token' }, true)
        }),
      )
      this.log.info('discovered accounts via token', { viewer: (me as { login: string }).login, count: accounts.length })
      return accounts
    }

    const app = this.getApp()
    const accounts: DiscoveredAccount[] = []
    for await (const { installation } of app.eachInstallation.iterator()) {
      const account = installation.account
      if (!account || !('login' in account)) continue
      const kit = await this.appClient(installation.id)
      const { data } = await kit.request('GET /users/{username}', { username: account.login })
      accounts.push(toAccount(data as GhAccount, { type: 'github-app', installationId: installation.id }, this.allowed(account.login)))
    }
    this.log.info('discovered accounts via app installations', { count: accounts.length })
    return accounts
  }

  async discoverRepos(account: AccountRow): Promise<DiscoveredRepo[]> {
    const kit = await this.clientFor(account)
    let repos: GhRepo[]
    if (account.connection.type === 'github-app') {
      repos = (await kit.paginate('GET /installation/repositories', { per_page: 100 })) as GhRepo[]
    } else if (account.kind === 'organization') {
      repos = (await kit.paginate('GET /orgs/{org}/repos', { org: account.login, type: 'all', per_page: 100 })) as GhRepo[]
    } else {
      const { data: me } = await kit.request('GET /user')
      repos =
        (me as { login: string }).login.toLowerCase() === account.login.toLowerCase()
          ? ((await kit.paginate('GET /user/repos', { affiliation: 'owner', per_page: 100 })) as GhRepo[])
          : ((await kit.paginate('GET /users/{username}/repos', { username: account.login, per_page: 100 })) as GhRepo[])
    }
    return repos.map((r) => ({ id: `github:${r.id}`, provider: 'github', accountId: account.id, fullName: r.full_name }))
  }

  async syncRepo(account: AccountRow, repoId: string, fullName: string): Promise<RepoSnapshot> {
    const snapshot = await syncGithubRepo(await this.clientFor(account), account.id, fullName, this.log)
    if (snapshot.id !== repoId) throw new Error(`Repo id changed from ${repoId} to ${snapshot.id}`)
    return snapshot
  }

  // Webhooks --------------------------------------------------------------------

  async handleWebhook(headers: Headers, body: string): Promise<WebhookOutcome> {
    const c = this.credentials
    if (!c || c.mode !== 'app' || !c.webhookSecret) throw new Error('Webhooks need a GitHub App with a webhook secret')
    const app = this.getApp()
    const outcome: WebhookOutcome = { repos: [], rediscover: false }
    const id = headers.get('x-github-delivery') ?? ''
    const name = headers.get('x-github-event') ?? ''
    const signature = headers.get('x-hub-signature-256') ?? ''

    const listener = ({ payload }: { payload: unknown }) => {
      const p = payload as { repository?: { full_name: string }; installation?: unknown }
      if (p.repository) outcome.repos.push(p.repository.full_name)
      if (name === 'installation' || name === 'installation_repositories' || name === 'repository') outcome.rediscover = true
    }
    app.webhooks.onAny(listener)
    try {
      await app.webhooks.verifyAndReceive({ id, name: name as never, signature, payload: body })
    } finally {
      app.webhooks.removeListener('*', listener)
    }
    this.log.info('webhook received', { event: name, delivery: id, repos: outcome.repos, rediscover: outcome.rediscover })
    return outcome
  }

  private allowed(login: string): boolean {
    return this.config.GITHUB_ALLOWED_ACCOUNTS.some((a) => a.toLowerCase() === login.toLowerCase())
  }

  // Clients ---------------------------------------------------------------------

  private getApp(): App {
    const c = this.credentials
    if (!c || c.mode !== 'app') throw new Error('GitHub App credentials are not configured')
    if (!this.app) {
      this.app = new App({
        appId: c.appId,
        privateKey: c.privateKey,
        webhooks: { secret: c.webhookSecret ?? 'unset' },
        Octokit: Octokit.defaults({ userAgent: 'redgreen' }),
      })
    }
    return this.app
  }

  private clientFor(account: AccountRow): Promise<Octokit> {
    return account.connection.type === 'github-app'
      ? this.appClient(account.connection.installationId)
      : Promise.resolve(this.tokenClient('token'))
  }

  /** A fresh client per call; the app caches installation tokens behind it. */
  private async appClient(installationId: number): Promise<Octokit> {
    const kit = await this.getApp().getInstallationOctokit(installationId)
    return withConditionalRequests(kit, `installation:${installationId}`, this.store, this.limits, this.log)
  }

  private tokenClient(scope: string): Octokit {
    const c = this.credentials
    if (!c || c.mode !== 'token') throw new Error('GitHub token is not configured')
    return withConditionalRequests(new Octokit({ auth: c.token, userAgent: 'redgreen' }), scope, this.store, this.limits, this.log)
  }
}

function toAccount(user: GhAccount, connection: DiscoveredAccount['connection'], preapproved: boolean): DiscoveredAccount {
  return {
    id: `github:${user.id}`,
    provider: 'github',
    login: user.login,
    name: user.name?.trim() || user.login,
    kind: user.type === 'Organization' ? 'organization' : 'user',
    avatarUrl: user.avatar_url,
    url: user.html_url,
    connection,
    preapproved,
  }
}
