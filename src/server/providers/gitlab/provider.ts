import { randomBytes, timingSafeEqual } from 'node:crypto'
import type { GitlabSetup } from '../../../shared/api.ts'
import type { RepoSnapshot } from '../../../shared/model.ts'
import type { Config } from '../../config.ts'
import type { Logger } from '../../log.ts'
import type { SecretBox } from '../../secrets.ts'
import type { AccountRow, DiscoveredRepo, Store } from '../../store.ts'
import { isStatus } from '../errors.ts'
import type { DiscoveredAccount, Provider, ProviderStatus, WebhookOutcome } from '../provider.ts'
import { RateLimits } from '../rate-limits.ts'
import { GitlabClient, encodePath, type AuthHeaders } from './client.ts'
import type { GlGroup, GlProject, GlUser } from './normalize.ts'
import { authorizeUrl, exchangeCode, newState, refresh, type TokenSet } from './oauth.ts'
import { syncGitlabRepo } from './repo-sync.ts'

const APP_SETTING = 'gitlab.app'
const TOKEN_SETTING = 'gitlab.token'
const STATE_SETTING = 'gitlab.oauthState'
const WEBHOOK_SETTING = 'gitlab.webhookSecret'

/** Refresh this long before the access token expires, so a sync never straddles the expiry. */
const REFRESH_AHEAD_MS = 5 * 60 * 1000
/** An authorize round trip older than this is not accepted back. */
const STATE_TTL_MS = 15 * 60 * 1000

/** An OAuth application entered on the settings page, stored encrypted. */
interface StoredApp {
  clientId: string
  clientSecret: string
}

/** The authorized user's tokens, stored encrypted. */
interface StoredToken {
  accessToken: string
  refreshToken: string
  expiresAt: string
  username: string
}

type Credentials =
  | { mode: 'oauth'; source: 'environment' | 'setup'; clientId: string; clientSecret: string; token: (TokenSet & { username: string }) | null }
  | { mode: 'token'; token: string; accounts: string[] }

export class GitlabProvider implements Provider {
  readonly kind = 'gitlab' as const
  private credentials: Credentials | null = null
  private refreshing: Promise<TokenSet> | null = null
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

  /** Re-reads credentials. An OAuth app from the environment wins over one from the setup flow, which wins over a token. */
  reload(): void {
    const { config } = this
    const token = this.storedToken()
    if (config.GITLAB_CLIENT_ID && config.GITLAB_CLIENT_SECRET) {
      this.credentials = { mode: 'oauth', source: 'environment', clientId: config.GITLAB_CLIENT_ID, clientSecret: config.GITLAB_CLIENT_SECRET, token }
      this.log.info('gitlab credentials: oauth app from environment', { url: config.GITLAB_URL, authorized: token !== null })
      return
    }
    const app = this.store.getSetting<StoredApp>(APP_SETTING)
    if (app) {
      this.credentials = { mode: 'oauth', source: 'setup', clientId: app.clientId, clientSecret: this.secrets.open(app.clientSecret), token }
      this.log.info('gitlab credentials: oauth app from setup flow', { url: config.GITLAB_URL, authorized: token !== null })
      return
    }
    if (config.GITLAB_TOKEN) {
      this.credentials = { mode: 'token', token: config.GITLAB_TOKEN, accounts: config.GITLAB_TOKEN_ACCOUNTS }
      this.log.info('gitlab credentials: access token', { url: config.GITLAB_URL, accounts: config.GITLAB_TOKEN_ACCOUNTS })
      return
    }
    this.credentials = null
    this.log.warn('gitlab credentials: none; waiting for setup')
  }

  configured(): boolean {
    const c = this.credentials
    return c !== null && (c.mode === 'token' || c.token !== null)
  }

  status(): ProviderStatus {
    const c = this.credentials
    const links: ProviderStatus['links'] = []
    if (c?.mode === 'oauth' && c.token) links.push({ label: `Groups of ${c.token.username}`, url: `${this.baseUrl()}/dashboard/groups` })
    return {
      kind: 'gitlab',
      configured: this.configured(),
      source: !this.configured() || !c ? null : c.mode === 'token' ? 'access token' : `OAuth (${c.source}) as ${c.token?.username}`,
      links,
      rateLimits: this.limits.list(),
    }
  }

  // Setup flow -----------------------------------------------------------------

  setup(): GitlabSetup {
    const c = this.credentials
    return {
      url: this.baseUrl(),
      redirectUri: this.redirectUri(),
      app: c?.mode === 'oauth' ? { source: c.source, clientId: c.clientId } : null,
      connectedAs: c?.mode === 'oauth' ? (c.token?.username ?? null) : null,
      webhookUrl: `${this.publicUrl()}/api/webhooks/gitlab`,
      webhookSecret: this.configured() ? this.webhookSecret() : null,
    }
  }

  /** Stores an OAuth application entered on the settings page. Authorizing happens next, in the browser. */
  saveApp(clientId: string, clientSecret: string): void {
    if (this.config.GITLAB_CLIENT_ID) throw new Error('The GitLab application comes from the environment')
    const app: StoredApp = { clientId, clientSecret: this.secrets.seal(clientSecret) }
    this.store.setSetting(APP_SETTING, app)
    this.store.deleteSetting(TOKEN_SETTING)
    this.log.info('gitlab oauth application saved', { clientId })
    this.reload()
  }

  /** Where to send the browser to authorize. Remembers the state to check on the way back. */
  startAuthorize(): string {
    const c = this.credentials
    if (c?.mode !== 'oauth') throw new Error('Enter a GitLab OAuth application first')
    const state = newState()
    this.store.setSetting(STATE_SETTING, { state, createdAt: new Date().toISOString() })
    this.log.info('gitlab authorize started')
    return authorizeUrl(this.baseUrl(), c.clientId, this.redirectUri(), state)
  }

  /** Finishes the authorize round trip: checks the state, swaps the code for tokens, records who authorized. */
  async completeAuthorize(code: string, state: string): Promise<void> {
    const c = this.credentials
    if (c?.mode !== 'oauth') throw new Error('Enter a GitLab OAuth application first')
    const expected = this.store.getSetting<{ state: string; createdAt: string }>(STATE_SETTING)
    this.store.deleteSetting(STATE_SETTING)
    if (!expected || expected.state !== state || Date.parse(expected.createdAt) < Date.now() - STATE_TTL_MS) {
      this.log.warn('gitlab authorize rejected: state mismatch or expired')
      throw new Error('The authorization did not start here, or took too long. Start again.')
    }
    const tokens = await exchangeCode(this.baseUrl(), c, this.redirectUri(), code)
    const me = await this.clientWith(async () => ({ authorization: `Bearer ${tokens.accessToken}` }), 'oauth').get<GlUser>('user')
    this.saveToken({ ...tokens, username: me.username })
    if (!this.config.GITLAB_WEBHOOK_SECRET && !this.store.getSetting<string>(WEBHOOK_SETTING)) {
      this.store.setSetting(WEBHOOK_SETTING, this.secrets.seal(randomBytes(24).toString('hex')))
    }
    this.log.info('gitlab authorized', { username: me.username })
    this.reload()
  }

  // Discovery -------------------------------------------------------------------

  async discoverAccounts(): Promise<DiscoveredAccount[]> {
    const c = this.credentials
    if (!c || !this.configured()) return []
    const client = this.client()
    if (c.mode === 'token') {
      const accounts = await Promise.all(c.accounts.map((path) => this.lookup(client, path, { type: 'gitlab-token' })))
      this.log.info('discovered accounts via token', { count: accounts.length })
      return accounts
    }

    const me = await client.get<GlUser>('user')
    const groups = topLevel(await client.paginate<GlGroup>('groups', { min_access_level: 10 }))
    const accounts: DiscoveredAccount[] = [
      toUserAccount(me, { type: 'gitlab-oauth' }, this.allowed(me.username)),
      ...groups.map((g) => toGroupAccount(g, { type: 'gitlab-oauth' }, this.allowed(g.full_path))),
    ]
    this.log.info('discovered accounts via oauth', { user: me.username, groups: groups.length })
    return accounts
  }

  async discoverRepos(account: AccountRow): Promise<DiscoveredRepo[]> {
    const client = this.client()
    const id = numericId(account.id)
    const projects =
      account.kind === 'organization'
        ? await client.paginate<GlProject>(`groups/${id}/projects`, { include_subgroups: true, with_shared: false, simple: true })
        : await client.paginate<GlProject>(`users/${id}/projects`, { simple: true })
    return projects.map((p) => ({ id: `gitlab:${p.id}`, provider: 'gitlab', accountId: account.id, fullName: p.path_with_namespace }))
  }

  async syncRepo(account: AccountRow, repoId: string, _fullName: string): Promise<RepoSnapshot> {
    return syncGitlabRepo(this.client(), account.id, numericId(repoId), this.log)
  }

  // Webhooks --------------------------------------------------------------------

  /** Accepts project or group webhooks a person pointed at redgreen, checked by the secret token shown on the settings page. */
  async handleWebhook(headers: Headers, body: string): Promise<WebhookOutcome> {
    const secret = this.webhookSecret()
    if (!secret) throw new Error('GitLab is not connected')
    const given = headers.get('x-gitlab-token') ?? ''
    if (given.length !== secret.length || !timingSafeEqual(Buffer.from(given), Buffer.from(secret))) throw new Error('Bad webhook token')
    const event = headers.get('x-gitlab-event') ?? ''
    const payload = JSON.parse(body) as { project?: { path_with_namespace?: string } }
    const repos = payload.project?.path_with_namespace ? [payload.project.path_with_namespace] : []
    const outcome: WebhookOutcome = { repos, rediscover: false }
    this.log.info('webhook received', { event, repos: outcome.repos, rediscover: outcome.rediscover })
    return outcome
  }

  // Clients ---------------------------------------------------------------------

  private client(): GitlabClient {
    const c = this.credentials
    if (!c) throw new Error('GitLab is not configured')
    if (c.mode === 'token') return this.clientWith(async () => ({ 'private-token': c.token }), 'token')
    return this.clientWith(async () => ({ authorization: `Bearer ${await this.accessToken()}` }), 'oauth')
  }

  private clientWith(auth: AuthHeaders, scope: string): GitlabClient {
    return new GitlabClient(this.baseUrl(), auth, scope, this.store, this.limits, this.log)
  }

  /** The current access token, refreshed when it is about to expire. Concurrent callers share one refresh. */
  private async accessToken(): Promise<string> {
    const c = this.credentials
    if (c?.mode !== 'oauth' || !c.token) throw new Error('GitLab is not authorized')
    if (Date.parse(c.token.expiresAt) - Date.now() > REFRESH_AHEAD_MS) return c.token.accessToken
    if (!this.refreshing) {
      this.refreshing = this.refreshToken(c, c.token).finally(() => {
        this.refreshing = null
      })
    }
    return (await this.refreshing).accessToken
  }

  private async refreshToken(c: Credentials & { mode: 'oauth' }, current: TokenSet & { username: string }): Promise<TokenSet> {
    try {
      const tokens = await refresh(this.baseUrl(), c, this.redirectUri(), current.refreshToken)
      this.saveToken({ ...tokens, username: current.username })
      c.token = { ...tokens, username: current.username }
      this.log.info('gitlab token refreshed', { expiresAt: tokens.expiresAt })
      return tokens
    } catch (error) {
      // A refresh token GitLab no longer accepts cannot be retried; the person has to authorize again.
      this.store.deleteSetting(TOKEN_SETTING)
      c.token = null
      this.log.error('gitlab token refresh failed; authorization dropped', { error })
      throw error
    }
  }

  private saveToken(token: TokenSet & { username: string }): void {
    const stored: StoredToken = {
      accessToken: this.secrets.seal(token.accessToken),
      refreshToken: this.secrets.seal(token.refreshToken),
      expiresAt: token.expiresAt,
      username: token.username,
    }
    this.store.setSetting(TOKEN_SETTING, stored)
  }

  private storedToken(): (TokenSet & { username: string }) | null {
    const stored = this.store.getSetting<StoredToken>(TOKEN_SETTING)
    if (!stored) return null
    return {
      accessToken: this.secrets.open(stored.accessToken),
      refreshToken: this.secrets.open(stored.refreshToken),
      expiresAt: stored.expiresAt,
      username: stored.username,
    }
  }

  private webhookSecret(): string | null {
    if (this.config.GITLAB_WEBHOOK_SECRET) return this.config.GITLAB_WEBHOOK_SECRET
    const stored = this.store.getSetting<string>(WEBHOOK_SETTING)
    return stored ? this.secrets.open(stored) : null
  }

  /** A group or user by path, for token mode where the person names the namespaces. */
  private async lookup(client: GitlabClient, path: string, connection: DiscoveredAccount['connection']): Promise<DiscoveredAccount> {
    try {
      const group = await client.get<GlGroup>(`groups/${encodePath(path)}`, { with_projects: false })
      return toGroupAccount(group, connection, true)
    } catch (error) {
      if (!isStatus(error, 404)) throw error
    }
    const [user] = await client.get<GlUser[]>('users', { username: path })
    if (!user) throw new Error(`No GitLab group or user at ${path}`)
    return toUserAccount(user, connection, true)
  }

  private allowed(path: string): boolean {
    return this.config.GITLAB_ALLOWED_ACCOUNTS.some((a) => a.toLowerCase() === path.toLowerCase())
  }

  private baseUrl(): string {
    return this.config.GITLAB_URL.replace(/\/$/, '')
  }

  private publicUrl(): string {
    return this.config.PUBLIC_URL.replace(/\/$/, '')
  }

  private redirectUri(): string {
    return `${this.publicUrl()}/setup/gitlab/callback`
  }
}

/** Groups the person is in, minus any nested under another group in the list; the parent's projects already cover them. */
export function topLevel(groups: GlGroup[]): GlGroup[] {
  const paths = groups.map((g) => g.full_path)
  return groups.filter((g) => !paths.some((p) => g.full_path.startsWith(`${p}/`)))
}

function numericId(id: string): number {
  const n = Number(id.slice(id.lastIndexOf(':') + 1))
  if (!Number.isInteger(n)) throw new Error(`Not a GitLab id: ${id}`)
  return n
}

function toGroupAccount(group: GlGroup, connection: DiscoveredAccount['connection'], preapproved: boolean): DiscoveredAccount {
  return {
    id: `gitlab:group:${group.id}`,
    provider: 'gitlab',
    login: group.full_path,
    name: group.name,
    kind: 'organization',
    avatarUrl: group.avatar_url,
    url: group.web_url,
    connection,
    preapproved,
  }
}

function toUserAccount(user: GlUser, connection: DiscoveredAccount['connection'], preapproved: boolean): DiscoveredAccount {
  return {
    id: `gitlab:user:${user.id}`,
    provider: 'gitlab',
    login: user.username,
    name: user.name?.trim() || user.username,
    kind: 'user',
    avatarUrl: user.avatar_url,
    url: user.web_url,
    connection,
    preapproved,
  }
}
