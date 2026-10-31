// Keeps every deployment the board learns of, reads the commits each one
// carried through the forge's compare endpoint, and computes the four
// DORA measures from what is kept. The measures themselves are the pure
// functions in shared/outcomes.ts; this file is the plumbing around them.

import type { OutcomesResponse } from '../../shared/api.ts'
import type { Release, RepoSnapshot } from '../../shared/model.ts'
import { measure, classify, outcomes, type DeploymentRecord, type Measures, type WindowDays } from '../../shared/outcomes.ts'
import type { Logger } from '../log.ts'
import type { Provider } from '../providers/provider.ts'
import type { AccountRow, RepoRow, Store } from '../store.ts'
import type { Telemetry } from '../telemetry.ts'
import { incidentSourcesConfigured, incidentsSince } from './incidents.ts'

const DAY_MS = 86_400_000
/** Deployments are kept this long: the longest window and the one before it, with room. */
export const KEEP_DAYS = 200
/** Compares made per repository per sync. The rest wait for the next one, so a backlog never starves a sweep. */
export const COMMIT_READS_PER_SYNC = 10

export class Outcomes {
  private readonly store: Store
  private readonly providers: Provider[]
  private readonly telemetry: Telemetry
  private readonly log: Logger
  /** Set once a reader of another source is on, so the page can say so. */
  orreryOn = false

  constructor(store: Store, providers: Provider[], telemetry: Telemetry, log: Logger) {
    this.store = store
    this.providers = providers
    this.telemetry = telemetry
    this.log = log
  }

  /** Keeps the finished deployments a forge recorded for a repo, then reads the commits of any whose commits are unread. */
  async ingestRepo(row: RepoRow, snapshot: RepoSnapshot, account: AccountRow): Promise<void> {
    const kept = this.keep(forgeRecords(row.id, snapshot))
    if (kept > 0) this.log.info('forge deployments kept', { repo: snapshot.fullName, kept })
    await this.readCommits(row.id, snapshot, account)
  }

  /** Keeps deployments from a source outside the forge. Answers how many were new. */
  keep(records: DeploymentRecord[]): number {
    const now = new Date().toISOString()
    let fresh = 0
    for (const r of records) {
      const seen = this.store.hasDeployment(r.key)
      this.store.upsertDeployment(r, now)
      if (!seen) {
        fresh++
        this.telemetry.deploymentIngested(r.source, r.status)
      }
    }
    return fresh
  }

  /** Reads the carried commits of a repo's unread deployments, a bounded number per call, through its provider. */
  async readCommits(repoId: string, snapshot?: RepoSnapshot, account?: AccountRow): Promise<void> {
    const row = this.store.getRepo(repoId)
    const snap = snapshot ?? row?.snapshot ?? null
    const acct = account ?? (row ? this.store.getAccount(row.accountId) : null)
    const provider = row && this.providers.find((p) => p.kind === row.provider)
    if (!row || !snap || !acct || !provider) return
    const unread = this.store.deploymentsWithoutCommits(repoId, COMMIT_READS_PER_SYNC)
    if (unread.length === 0) return
    const all = this.store.listDeployments(repoId)
    let read = 0
    for (const d of unread) {
      const previous = all.filter((p) => p.key !== d.key && p.service === d.service && p.environment === d.environment && p.status === 'succeeded' && p.finishedAt < d.finishedAt).at(-1) ?? null
      const head = d.revisionElsewhere ? standIn(snap.releases, d.finishedAt) : d.revision
      const base = previous === null ? null : previous.revisionElsewhere ? standIn(snap.releases, previous.finishedAt) : previous.revision
      if (head === null) {
        this.log.info('deployment carries no known release; its commits stay unknown', { repo: snap.fullName, deployment: d.key })
        this.store.setDeploymentCommits(d.key, null, [])
        continue
      }
      if (head === base) {
        this.store.setDeploymentCommits(d.key, base, [])
        continue
      }
      try {
        const commits = await provider.commitsBetween(acct, snap, base, head)
        if (commits === null) this.log.info('commits between revisions unknown; the deployment counts without a lead time', { repo: snap.fullName, deployment: d.key, base, head })
        this.store.setDeploymentCommits(d.key, base, commits ?? [])
        read++
      } catch (error) {
        this.log.warn('commits between revisions could not be read; tried again next sync', { repo: snap.fullName, deployment: d.key, error })
      }
    }
    if (read > 0) this.log.info('deployment commits read', { repo: snap.fullName, read, unread: unread.length - read })
  }

  /** Drops deployments older than the windows need. */
  prune(now = Date.now()): number {
    return this.store.pruneDeployments(new Date(now - KEEP_DAYS * DAY_MS).toISOString())
  }

  /** The page: the estate and one row per repository and service, over the window. */
  async compute(window: WindowDays, now = Date.now()): Promise<OutcomesResponse> {
    const since = new Date(now - 2 * window * DAY_MS).toISOString()
    const incidents = await incidentsSince(since)
    return this.telemetry.compute(window, () => {
      const rows = new Map(this.store.listRepos().map((r) => [r.id, r]))
      const names = (repoId: string, service: string) => {
        const row = rows.get(repoId)
        if (!row) return null
        const found = row.snapshot?.services?.services.find((s) => s.path === service)
        const fullName = row.snapshot?.fullName ?? row.fullName
        return { fullName, name: found?.name ?? (service ? service.split('/').at(-1)! : fullName.split('/').at(-1)!) }
      }
      const { estate, rows: result } = outcomes({ records: this.store.listDeployments(null, since), incidents, names, window, now })
      return { generatedAt: new Date(now).toISOString(), window, estate, rows: result, sources: { forge: true, orrery: this.orreryOn, incidents: incidentSourcesConfigured() } }
    })
  }

  /** The last 30 days of one repository, whole and per service folder, for the catalog push. */
  measuresFor(repoId: string, now = Date.now()): { repo: Measures; services: Map<string, Measures> } {
    const from = now - 30 * DAY_MS
    const classified = classify(this.store.listDeployments(repoId, new Date(from).toISOString()))
    const services = new Map<string, Measures>()
    for (const service of new Set(classified.map((d) => d.service))) services.set(service, measure(classified.filter((d) => d.service === service), from, now))
    return { repo: measure(classified, from, now), services }
  }
}

/** The forge's finished deployments as records of the whole repository. */
export function forgeRecords(repoId: string, snapshot: RepoSnapshot): DeploymentRecord[] {
  return (snapshot.deployments ?? [])
    .filter((d) => (d.status === 'succeeded' || d.status === 'failed') && d.finishedAt !== null)
    .map((d) => ({
      key: `${repoId}/forge/${d.id}`,
      repoId,
      service: '',
      source: 'forge',
      environment: d.environment,
      status: d.status === 'succeeded' ? 'succeeded' : 'failed',
      revision: d.sha,
      revisionElsewhere: false,
      startedAt: d.createdAt,
      finishedAt: d.finishedAt!,
      url: d.url,
      baseRevision: null,
      commits: null,
    }))
}

/** The newest release published at or before `at`, whose tag stands in for a revision that lives in another repository. */
export function standIn(releases: Release[], at: string): string | null {
  return releases.filter((r) => !r.prerelease && r.publishedAt <= at).sort((a, b) => b.publishedAt.localeCompare(a.publishedAt))[0]?.tag ?? null
}
