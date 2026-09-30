import type { SyncStatus } from '../../shared/api.ts'
import type { ProviderKind } from '../../shared/model.ts'
import type { Config } from '../config.ts'
import type { Estate } from '../estate.ts'
import type { Events } from '../events.ts'
import type { Logger } from '../log.ts'
import type { Provider } from '../providers/provider.ts'
import type { AccountRow, Store } from '../store.ts'

/** How long a webhook-triggered refresh waits for more events about the same repo. */
const WEBHOOK_DEBOUNCE_MS = 3_000
const CACHE_TTL_DAYS = 7

type Priority = 'high' | 'normal'

interface Job {
  repoId: string
  reason: string
  priority: Priority
}

/**
 * Keeps snapshots fresh. A sweep rediscovers accounts and repos and queues
 * every repo; webhooks queue single repos ahead of the sweep. One queue, one
 * pool of workers, one job per repo at a time.
 */
export class Scheduler {
  private readonly providers: Provider[]
  private readonly store: Store
  private readonly estate: Estate
  private readonly events: Events
  private readonly config: Config
  private readonly log: Logger

  private readonly queue: Job[] = []
  private readonly inFlight = new Set<string>()
  private readonly debounces = new Map<string, NodeJS.Timeout>()
  private timer: NodeJS.Timeout | null = null
  private sweeping: Promise<void> | null = null
  private stopped = false
  private status: SyncStatus = {
    sweeping: false,
    lastSweepStartedAt: null,
    lastSweepFinishedAt: null,
    queued: 0,
    inFlight: 0,
    lastError: null,
  }

  constructor(providers: Provider[], store: Store, estate: Estate, events: Events, config: Config, log: Logger) {
    this.providers = providers
    this.store = store
    this.estate = estate
    this.events = events
    this.config = config
    this.log = log
  }

  start(): void {
    const interval = this.config.SYNC_INTERVAL_MINUTES * 60_000
    this.timer = setInterval(() => void this.sweep('interval'), interval)
    void this.sweep('startup')
  }

  stop(): void {
    this.stopped = true
    if (this.timer) clearInterval(this.timer)
    for (const t of this.debounces.values()) clearTimeout(t)
  }

  currentStatus(): SyncStatus {
    return { ...this.status, queued: this.queue.length, inFlight: this.inFlight.size }
  }

  /** Discovers accounts and repos, then queues every repo. Concurrent calls share one sweep. */
  sweep(reason: string): Promise<void> {
    if (this.sweeping) return this.sweeping
    this.sweeping = this.runSweep(reason).finally(() => {
      this.sweeping = null
    })
    return this.sweeping
  }

  /** Queues one repo ahead of the sweep. Repeated calls within the debounce window collapse. */
  refreshRepo(repoId: string, reason: string): void {
    const pending = this.debounces.get(repoId)
    if (pending) clearTimeout(pending)
    this.debounces.set(
      repoId,
      setTimeout(() => {
        this.debounces.delete(repoId)
        this.enqueue({ repoId, reason, priority: 'high' })
      }, WEBHOOK_DEBOUNCE_MS),
    )
  }

  /** Runs one repo now, outside the debounce, and resolves when it is done. */
  async syncNow(repoId: string): Promise<void> {
    await this.syncOne({ repoId, reason: 'manual', priority: 'high' })
  }

  refreshByFullName(provider: ProviderKind, fullName: string, reason: string): boolean {
    const row = this.store.findRepoByFullName(provider, fullName)
    if (!row) {
      this.log.info('webhook named a repo that is not tracked', { provider, repo: fullName })
      return false
    }
    this.refreshRepo(row.id, reason)
    return true
  }

  private async runSweep(reason: string): Promise<void> {
    const startedAt = new Date().toISOString()
    this.status = { ...this.status, sweeping: true, lastSweepStartedAt: startedAt, lastError: null }
    this.publishStatus()
    this.log.info('sweep started', { reason })
    try {
      for (const provider of this.providers) {
        if (!provider.configured()) {
          this.log.info('provider not configured; skipped', { provider: provider.kind })
          continue
        }
        await this.discover(provider, startedAt)
      }
      const pruned = this.store.pruneCache(new Date(Date.now() - CACHE_TTL_DAYS * 86_400_000).toISOString())
      if (pruned > 0) this.log.info('pruned http cache', { entries: pruned })
    } catch (error) {
      this.status = { ...this.status, lastError: String(error instanceof Error ? error.message : error) }
      this.log.error('sweep failed', { reason, error })
    } finally {
      this.status = { ...this.status, sweeping: false, lastSweepFinishedAt: new Date().toISOString() }
      this.publishStatus()
      this.events.emit({ type: 'estate' })
      this.log.info('sweep finished', { reason, queued: this.queue.length })
    }
  }

  private async discover(provider: Provider, startedAt: string): Promise<void> {
    const discovered = await provider.discoverAccounts()
    const accounts: AccountRow[] = []
    for (const found of discovered) {
      const existing = this.store.getAccount(found.id)
      const status = existing?.status ?? (found.preapproved ? 'active' : 'pending')
      if (!existing) this.log.info('account discovered', { provider: provider.kind, login: found.login, status })
      accounts.push(this.store.upsertAccount({ ...found, status }, new Date().toISOString()))
    }
    for (const id of this.store.pruneAccounts(provider.kind, startedAt)) this.log.info('account removed', { id })

    for (const account of accounts) {
      if (account.status !== 'active') {
        this.log.info('account not active; skipped', { login: account.login, status: account.status })
        continue
      }
      const seenAt = new Date().toISOString()
      try {
        const repos = await provider.discoverRepos(account)
        for (const repo of repos) this.store.upsertDiscoveredRepo(repo, seenAt)
        const removed = this.store.pruneRepos(account.id, seenAt)
        this.store.setAccountError(account.id, null)
        this.log.info('account discovered repos', { login: account.login, repos: repos.length, removed: removed.length })
        for (const repo of repos) this.enqueue({ repoId: repo.id, reason: 'sweep', priority: 'normal' })
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        this.store.setAccountError(account.id, message)
        this.log.error('account discovery failed', { login: account.login, error })
      }
    }
  }

  private enqueue(job: Job): void {
    if (this.stopped) return
    const existing = this.queue.findIndex((j) => j.repoId === job.repoId)
    if (existing !== -1) {
      if (job.priority === 'high' && this.queue[existing]?.priority !== 'high') {
        this.queue.splice(existing, 1)
        this.queue.unshift(job)
      }
      return
    }
    if (job.priority === 'high') this.queue.unshift(job)
    else this.queue.push(job)
    this.pump()
  }

  private pump(): void {
    while (this.inFlight.size < this.config.SYNC_CONCURRENCY) {
      const next = this.queue.findIndex((j) => !this.inFlight.has(j.repoId))
      if (next === -1) break
      const [job] = this.queue.splice(next, 1)
      if (!job) break
      void this.syncOne(job).finally(() => this.pump())
    }
    this.publishStatus()
  }

  private async syncOne(job: Job): Promise<void> {
    const row = this.store.getRepo(job.repoId)
    if (!row) return
    const account = this.store.getAccount(row.accountId)
    const provider = this.providers.find((p) => p.kind === row.provider)
    if (!account || !provider) {
      this.log.warn('repo has no account or provider; skipped', { repo: row.fullName })
      return
    }
    this.inFlight.add(job.repoId)
    const started = Date.now()
    try {
      const snapshot = await provider.syncRepo(account, row.id, row.fullName)
      this.store.saveSnapshot(snapshot)
      const level = this.estate.reconcileLevel({ ...row, snapshot, syncedAt: snapshot.syncedAt, syncError: null })
      this.log.info('repo synced', { repo: snapshot.fullName, reason: job.reason, health: level, ms: Date.now() - started })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      this.store.saveSyncError(row.id, message)
      this.log.error('repo sync failed', { repo: row.fullName, reason: job.reason, error })
    } finally {
      this.inFlight.delete(job.repoId)
      this.events.emit({ type: 'repo', id: job.repoId })
    }
  }

  private publishStatus(): void {
    this.events.emit({ type: 'sync', status: this.currentStatus() })
  }
}
