import type { AzureDevopsSetup } from '../../../shared/api.ts'
import type { RepoSnapshot } from '../../../shared/model.ts'
import type { Config } from '../../config.ts'
import type { Logger } from '../../log.ts'
import type { SecretBox } from '../../secrets.ts'
import type { AccountRow, DiscoveredRepo, Store } from '../../store.ts'
import { isStatus } from '../errors.ts'
import type { RepoFiles } from '../../services.ts'
import type { DiscoveredAccount, Provider, ProviderStatus } from '../provider.ts'
import { RateLimits } from '../rate-limits.ts'
import { AzureDevopsClient } from './client.ts'
import { azureDevopsFiles } from './files.ts'
import { requestToken, type AccessToken, type ServicePrincipal } from './entra.ts'
import { topLanguages, type AdoLanguageMetrics, type AdoRepo } from './normalize.ts'
import { AZURE_DEVOPS_URL, syncAzureDevopsRepo } from './repo-sync.ts'

const APP_SETTING = 'azuredevops.app'

/** Ask for a new token this long before the current one expires, so a sync never straddles the expiry. */
const RENEW_AHEAD_MS = 5 * 60 * 1000
/** How long a project's language breakdown is reused. It covers every repository in the project and changes slowly. */
const LANGUAGES_TTL_MS = 15 * 60 * 1000

/** A service principal entered on the settings page, stored with its secret encrypted. */
interface StoredApp {
  tenantId: string
  clientId: string
  clientSecret: string
  organizations: string[]
}

interface Credentials extends ServicePrincipal {
  source: 'environment' | 'setup'
  organizations: string[]
}

export interface AppInput {
  tenantId: string
  clientId: string
  /** Empty keeps the secret already on file. */
  clientSecret: string
  organizations: string[]
}

export class AzureDevopsProvider implements Provider {
  readonly kind = 'azuredevops' as const
  private credentials: Credentials | null = null
  private token: AccessToken | null = null
  private renewing: Promise<AccessToken> | null = null
  private readonly languages = new Map<string, { at: number; found: Promise<Map<string, string | null>> }>()
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

  /** Re-reads credentials. A service principal from the environment wins over one from the settings page. */
  reload(): void {
    const { config } = this
    this.token = null
    if (config.AZURE_DEVOPS_TENANT_ID && config.AZURE_DEVOPS_CLIENT_ID && config.AZURE_DEVOPS_CLIENT_SECRET) {
      this.credentials = {
        source: 'environment',
        tenantId: config.AZURE_DEVOPS_TENANT_ID,
        clientId: config.AZURE_DEVOPS_CLIENT_ID,
        clientSecret: config.AZURE_DEVOPS_CLIENT_SECRET,
        organizations: organizationNames(config.AZURE_DEVOPS_ORGANIZATIONS),
      }
      this.log.info('azure devops credentials: service principal from environment', { organizations: this.credentials.organizations })
      return
    }
    const app = this.store.getSetting<StoredApp>(APP_SETTING)
    if (app) {
      this.credentials = { source: 'setup', tenantId: app.tenantId, clientId: app.clientId, clientSecret: this.secrets.open(app.clientSecret), organizations: app.organizations }
      this.log.info('azure devops credentials: service principal from settings page', { organizations: app.organizations })
      return
    }
    this.credentials = null
    this.log.warn('azure devops credentials: none; waiting for setup')
  }

  configured(): boolean {
    return this.credentials !== null && this.credentials.organizations.length > 0
  }

  status(): ProviderStatus {
    const c = this.credentials
    return {
      kind: 'azuredevops',
      configured: this.configured(),
      source: c && this.configured() ? `service principal (${c.source})` : null,
      links: this.configured() && c ? c.organizations.map((org) => ({ label: org, url: organizationUrl(org) })) : [],
      rateLimits: this.limits.list(),
    }
  }

  // Setup flow -----------------------------------------------------------------

  setup(): AzureDevopsSetup {
    const c = this.credentials
    return { app: c ? { source: c.source, tenantId: c.tenantId, clientId: c.clientId } : null, organizations: c?.organizations ?? [] }
  }

  /** Stores a service principal entered on the settings page, once Entra has issued it a token. */
  async saveApp(input: AppInput): Promise<void> {
    if (this.credentials?.source === 'environment') throw new Error('The Azure DevOps service principal comes from the environment')
    const clientSecret = input.clientSecret || (this.credentials?.clientId === input.clientId ? this.credentials.clientSecret : '')
    if (!clientSecret) throw new Error('Enter the client secret')
    const organizations = organizationNames(input.organizations)
    if (organizations.length === 0) throw new Error('Name at least one organization')
    await requestToken({ tenantId: input.tenantId, clientId: input.clientId, clientSecret })
    const app: StoredApp = { tenantId: input.tenantId, clientId: input.clientId, clientSecret: this.secrets.seal(clientSecret), organizations }
    this.store.setSetting(APP_SETTING, app)
    this.log.info('azure devops service principal saved', { tenantId: input.tenantId, clientId: input.clientId, organizations })
    this.reload()
  }

  // Discovery -------------------------------------------------------------------

  /** The organizations a person named. Naming one is the approval, so none waits as pending. */
  async discoverAccounts(): Promise<DiscoveredAccount[]> {
    const c = this.credentials
    if (!c) return []
    this.log.info('discovered accounts from named organizations', { count: c.organizations.length })
    return c.organizations.map((org) => ({
      id: `azuredevops:org:${org.toLowerCase()}`,
      provider: 'azuredevops',
      login: org,
      name: org,
      kind: 'organization',
      avatarUrl: null,
      url: organizationUrl(org),
      connection: { type: 'azuredevops-service-principal' },
      preapproved: true,
    }))
  }

  async discoverRepos(account: AccountRow): Promise<DiscoveredRepo[]> {
    const repos = await this.client().list<AdoRepo>(`${organizationUrl(account.login)}/_apis/git/repositories`)
    return repos.map((r) => ({ id: `azuredevops:${r.id}`, provider: 'azuredevops', accountId: account.id, fullName: `${account.login}/${r.project.name}/${r.name}` }))
  }

  async syncRepo(account: AccountRow, repoId: string, _fullName: string): Promise<RepoSnapshot> {
    const org = account.login
    return syncAzureDevopsRepo(this.client(), account.id, org, repoId.slice(repoId.lastIndexOf(':') + 1), (projectId, id) => this.language(org, projectId, id), this.log)
  }

  async files(account: AccountRow, snapshot: RepoSnapshot): Promise<RepoFiles | null> {
    return azureDevopsFiles(this.client(), account.login, snapshot.id.slice(snapshot.id.lastIndexOf(':') + 1), snapshot.defaultBranch)
  }

  // Clients ---------------------------------------------------------------------

  private client(): AzureDevopsClient {
    if (!this.credentials) throw new Error('Azure DevOps is not configured')
    return new AzureDevopsClient(async () => ({ authorization: `Bearer ${await this.accessToken()}` }), 'service-principal', this.store, this.limits, this.log)
  }

  /** The current access token, renewed when it is about to expire. Concurrent callers share one request. */
  private async accessToken(): Promise<string> {
    const c = this.credentials
    if (!c) throw new Error('Azure DevOps is not configured')
    if (this.token && Date.parse(this.token.expiresAt) - Date.now() > RENEW_AHEAD_MS) return this.token.accessToken
    if (!this.renewing) {
      this.renewing = requestToken(c)
        .then((token) => {
          // Credentials may have been replaced while the request was out; a token for the old ones is not kept.
          if (this.credentials === c) this.token = token
          this.log.info('azure devops token issued', { expiresAt: token.expiresAt })
          return token
        })
        .catch((error: unknown) => {
          this.log.error('azure devops token request failed', { error })
          throw error
        })
        .finally(() => {
          this.renewing = null
        })
    }
    return (await this.renewing).accessToken
  }

  /** One request answers for every repository in a project, so it is shared by the repos synced in a sweep. */
  private async language(org: string, projectId: string, repoId: string): Promise<string | null> {
    const key = `${org}/${projectId}`
    let entry = this.languages.get(key)
    if (!entry || Date.now() - entry.at > LANGUAGES_TTL_MS) {
      entry = { at: Date.now(), found: this.fetchLanguages(org, projectId) }
      this.languages.set(key, entry)
      entry.found.catch(() => this.languages.delete(key))
    }
    return (await entry.found).get(repoId) ?? null
  }

  private async fetchLanguages(org: string, projectId: string): Promise<Map<string, string | null>> {
    try {
      const url = `${organizationUrl(org)}/${projectId}/_apis/projectanalysis/languagemetrics`
      return topLanguages(await this.client().get<AdoLanguageMetrics>(url, { 'api-version': '7.1-preview.1' }))
    } catch (error) {
      if (!isStatus(error, 403, 404)) throw error
      this.log.debug('language metrics unavailable', { org, project: projectId })
      return new Map()
    }
  }
}

/** An organization's name, from the name itself or from its URL. */
export function organizationName(input: string): string {
  return input
    .trim()
    .replace(/^https?:\/\/dev\.azure\.com\//i, '')
    .replace(/^https?:\/\/([^./]+)\.visualstudio\.com.*$/i, '$1')
    .replace(/\/.*$/, '')
}

/** Names from a list a person typed, each once however it was cased. */
function organizationNames(inputs: string[]): string[] {
  const names = new Map<string, string>()
  for (const name of inputs.map(organizationName).filter(Boolean)) if (!names.has(name.toLowerCase())) names.set(name.toLowerCase(), name)
  return [...names.values()]
}

function organizationUrl(org: string): string {
  return `${AZURE_DEVOPS_URL}/${encodeURIComponent(org)}`
}
