import type { ProviderStatus } from '../../shared/api.ts'
import type { ProviderKind, RepoSnapshot } from '../../shared/model.ts'
import type { AccountRow, Connection, DiscoveredRepo } from '../store.ts'

export type { ProviderStatus, RateLimit } from '../../shared/api.ts'

export interface DiscoveredAccount {
  id: string
  provider: ProviderKind
  login: string
  name: string
  kind: AccountRow['kind']
  avatarUrl: string | null
  url: string
  connection: Connection
  /** True when the account may sync without a person approving it in the UI. */
  preapproved: boolean
}

/** What a webhook asks the scheduler to do. */
export interface WebhookOutcome {
  /** Full names of repos to refresh. */
  repos: string[]
  /** True when accounts or repo lists may have changed. */
  rediscover: boolean
}

/** A forge. Everything past this interface is provider-neutral. */
export interface Provider {
  readonly kind: ProviderKind
  configured(): boolean
  status(): ProviderStatus
  discoverAccounts(): Promise<DiscoveredAccount[]>
  discoverRepos(account: AccountRow): Promise<DiscoveredRepo[]>
  syncRepo(account: AccountRow, repoId: string, fullName: string): Promise<RepoSnapshot>
  /** Verifies and interprets a webhook delivery. Absent for providers that only poll. */
  handleWebhook?(headers: Headers, body: string): Promise<WebhookOutcome>
}
