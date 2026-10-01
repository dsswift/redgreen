// Pushes every repository to an Orrery hub as a `service` entity, so the
// catalog there lists what this board watches. One snapshot run per account;
// a single repo is patched as soon as it syncs.

import type { RepoSnapshot } from '../shared/model.ts'
import type { Level, RepoHealth } from '../shared/rules.ts'
import type { Config } from './config.ts'
import type { Estate } from './estate.ts'
import type { Events } from './events.ts'
import type { Logger } from './log.ts'
import type { AccountRow, Store } from './store.ts'

export interface ServiceEntity {
  blueprint: 'service'
  key: string
  title: string
  fields: Record<string, unknown>
}

const LEVEL: Record<Level, string> = { red: 'red', amber: 'amber', green: 'green', quiet: 'grey', unknown: 'grey' }
const STALE_PR_MS = 7 * 86_400_000

/** Shapes one repo. Pure, so it is tested without a hub. */
export function toService(repo: RepoSnapshot, account: Pick<AccountRow, 'login'>, health: RepoHealth | null, boardUrl: string, now = Date.now()): ServiceEntity {
  const decisive = repo.pipelines.flatMap((p) => p.runs).filter((r) => r.status === 'completed' && r.conclusion !== 'cancelled' && r.conclusion !== 'skipped' && r.ref === repo.defaultBranch)
  const failed = decisive.filter((r) => r.conclusion !== 'success' && r.conclusion !== 'neutral').length
  const failing = repo.pipelines.filter((p) => {
    const last = p.runs.find((r) => r.status === 'completed' && r.ref === repo.defaultBranch && r.conclusion !== 'cancelled' && r.conclusion !== 'skipped')
    return !!last && last.conclusion !== 'success' && last.conclusion !== 'neutral'
  }).length
  const release = repo.releases[0]
  const worst = health?.signals.find((s) => s.severity === health.level) ?? health?.signals[0]
  const fields: Record<string, unknown> = {
    description: repo.description,
    url: repo.url,
    provider: repo.provider,
    organization: account.login,
    name: repo.name,
    language: repo.language,
    visibility: repo.visibility,
    archived: repo.archived,
    fork: repo.fork,
    default_branch: repo.defaultBranch,
    last_push: repo.pushedAt,
    open_prs: repo.openPullRequests,
    stale_prs: repo.pullRequests.filter((p) => now - Date.parse(p.updatedAt) > STALE_PR_MS).length,
    open_issues: repo.openIssues,
    stars: repo.stars,
    pipelines: repo.pipelines.length,
    pipelines_failing: failing,
    workflow_failure_rate: decisive.length ? Math.round((failed / decisive.length) * 100) : null,
    has_ci: repo.pipelines.length > 0,
    last_release: release?.tag ?? null,
    last_release_at: release?.publishedAt ?? null,
    dependabot_critical: repo.security.dependabot?.critical ?? null,
    dependabot_high: repo.security.dependabot?.high ?? null,
    code_scanning_critical: repo.security.codeScanning?.critical ?? null,
    code_scanning_high: repo.security.codeScanning?.high ?? null,
    health: LEVEL[health?.level ?? 'unknown'],
    health_reason: health?.level === 'quiet' ? (health.quietReason ?? 'quiet') : (worst?.title ?? null),
    board: boardUrl,
  }
  if (repo.facts) {
    Object.assign(fields, {
      topics: repo.facts.topics,
      branch_protected: repo.facts.branchProtected,
      requires_code_review: repo.facts.requiresReview,
      required_approvals: repo.facts.requiredApprovals,
      requires_code_owner_review: repo.facts.requiresCodeOwnerReview,
      has_codeowners: repo.facts.hasCodeowners,
      has_readme: repo.facts.hasReadme,
      has_dockerfile: repo.facts.hasDockerfile,
      last_committer: repo.facts.lastCommitter,
      last_commit_at: repo.facts.lastCommitAt,
    })
  }
  return { blueprint: 'service', key: `${repo.provider}/${repo.fullName}`.toLowerCase(), title: repo.name, fields }
}

export class OrreryPusher {
  private readonly url: string
  private readonly token: string
  private readonly config: Config
  private readonly store: Store
  private readonly estate: Estate
  private readonly events: Events
  private readonly log: Logger
  private timer: NodeJS.Timeout | null = null
  private unsubscribe: (() => void) | null = null

  constructor(config: Config, store: Store, estate: Estate, events: Events, log: Logger) {
    this.url = (config.ORRERY_URL ?? '').replace(/\/$/, '')
    this.token = config.ORRERY_TOKEN ?? ''
    this.config = config
    this.store = store
    this.estate = estate
    this.events = events
    this.log = log
  }

  enabled(): boolean {
    return !!this.url && !!this.token
  }

  start(): void {
    if (!this.enabled()) {
      this.log.info('orrery push off: ORRERY_URL and ORRERY_TOKEN are not both set')
      return
    }
    this.log.info('orrery push on', { url: this.url })
    const interval = this.config.SYNC_INTERVAL_MINUTES * 60_000
    // The first sweep needs a head start, so the first snapshot is not half empty.
    setTimeout(() => void this.pushAll('startup'), 90_000).unref()
    this.timer = setInterval(() => void this.pushAll('interval'), interval)
    this.unsubscribe = this.events.subscribe((event) => {
      if (event.type === 'repo') void this.pushRepo(event.id)
    })
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer)
    this.unsubscribe?.()
  }

  private boardUrl(repoId: string): string {
    return `${this.config.PUBLIC_URL.replace(/\/$/, '')}/repos/${encodeURIComponent(repoId)}`
  }

  private async call(method: string, path: string, body?: unknown): Promise<unknown> {
    const res = await fetch(this.url + path, { method, headers: { authorization: `Bearer ${this.token}`, 'content-type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(60_000) })
    const text = await res.text()
    if (!res.ok && res.status !== 207) throw new Error(`orrery ${method} ${path}: ${res.status} ${text.slice(0, 300)}`)
    return text ? JSON.parse(text) : null
  }

  /** One repo, right after it synced. Merges fields; never deletes. */
  async pushRepo(repoId: string): Promise<void> {
    const row = this.store.getRepo(repoId)
    const account = row && this.store.getAccount(row.accountId)
    if (!row?.snapshot || !account || account.status !== 'active') return
    const entity = toService(row.snapshot, account, this.estate.healthOf(row), this.boardUrl(row.id))
    try {
      await this.call('PATCH', `/ingest/entities/service/${encodeURIComponent(entity.key)}`, { title: entity.title, fields: entity.fields, scope: `${account.provider}/${account.login}`.toLowerCase() })
      this.log.debug('orrery repo pushed', { repo: row.fullName })
    } catch (error) {
      this.log.warn('orrery repo push failed', { repo: row.fullName, error })
    }
  }

  /** Every active account as one run. A snapshot when every repo has synced; incremental otherwise, so nothing half read is deleted. */
  async pushAll(reason: string): Promise<void> {
    const rows = this.store.listRepos()
    for (const account of this.store.listAccounts()) {
      if (account.status !== 'active') continue
      const mine = rows.filter((r) => r.accountId === account.id)
      if (mine.length === 0) continue
      const synced = mine.filter((r) => r.snapshot)
      const mode = synced.length === mine.length ? 'snapshot' : 'incremental'
      const scope = `${account.provider}/${account.login}`.toLowerCase()
      try {
        const run = (await this.call('POST', '/ingest/runs', { mode, scope })) as { id: string }
        const entities = synced.map((r) => toService(r.snapshot!, account, this.estate.healthOf(r), this.boardUrl(r.id)))
        let rejected = 0
        for (let i = 0; i < entities.length; i += 100) {
          const res = (await this.call('PUT', `/ingest/runs/${encodeURIComponent(run.id)}/entities`, { entities: entities.slice(i, i + 100) })) as { rejected?: { key?: string; field?: string; reason: string }[] }
          for (const r of res.rejected ?? []) {
            rejected++
            this.log.warn('orrery rejected a service', { key: r.key, field: r.field, reason: r.reason })
          }
        }
        await this.call('POST', `/ingest/runs/${encodeURIComponent(run.id)}/commit`)
        this.log.info('orrery push done', { reason, account: account.login, mode, services: entities.length, unsynced: mine.length - synced.length, rejected })
      } catch (error) {
        this.log.error('orrery push failed', { reason, account: account.login, error })
      }
    }
  }
}
