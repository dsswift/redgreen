// Reads the deployments an Orrery hub holds, the ArgoCD syncs its cluster
// pusher records, and keeps each one against the repository it rolled out.
// A sync names an ArgoCD app; the app names the workload it deploys; the
// catalog gives the workload its repository, and a folder in it when the
// workload is one service of a monorepo. The sync's revision is a commit in
// the app's source repository, which for a workload built elsewhere is the
// deployment manifest, not the code; such a deployment is marked so the
// repository's release tags stand in for its revision when its commits are
// read. Reading the hub takes an access token: the source token that pushes
// may not read.

import type { DeploymentRecord } from '../../shared/outcomes.ts'
import type { Config } from '../config.ts'
import type { Logger } from '../log.ts'
import type { Store } from '../store.ts'
import type { Outcomes } from './outcomes.ts'
import { KEEP_DAYS } from './outcomes.ts'
import { parseRepoUrl, sameRepo, type RepoRef } from './repo-url.ts'

const DAY_MS = 86_400_000
const PAGE = 2000

interface Related {
  key: string
  title?: string
}

export interface OrreryDeployment {
  key: string
  fields: { status?: string | null; revision?: string | null; started_at?: string | null; deployed_at?: string | null }
  relations: { app?: Related[] }
}

export interface OrreryApp {
  key: string
  fields: { repo_url?: string | null }
  relations: { workload?: Related[]; environment?: Related[] }
}

export interface OrreryWorkload {
  key: string
  fields: { repo?: string | null }
}

export interface Mapped {
  records: DeploymentRecord[]
  /** Why syncs were left out, by reason. */
  skipped: Record<'no_app' | 'no_workload' | 'no_repo_url' | 'repo_not_tracked' | 'unfinished', number>
}

/** Turns the hub's syncs into records of the repositories this board tracks. Pure, so it is tested without a hub. */
export function mapOrreryDeployments(deployments: OrreryDeployment[], apps: OrreryApp[], workloads: OrreryWorkload[], resolve: (ref: RepoRef) => string | null, gitlabHost: string): Mapped {
  const appByKey = new Map(apps.map((a) => [a.key, a]))
  const workloadByKey = new Map(workloads.map((w) => [w.key, w]))
  const out: Mapped = { records: [], skipped: { no_app: 0, no_workload: 0, no_repo_url: 0, repo_not_tracked: 0, unfinished: 0 } }
  for (const d of deployments) {
    const status = d.fields.status
    const finishedAt = d.fields.deployed_at
    if ((status !== 'succeeded' && status !== 'failed') || !finishedAt || !d.fields.revision) {
      out.skipped.unfinished++
      continue
    }
    const app = d.relations.app?.[0] && appByKey.get(d.relations.app[0].key)
    if (!app) {
      out.skipped.no_app++
      continue
    }
    const workload = app.relations.workload?.[0] && workloadByKey.get(app.relations.workload[0].key)
    if (!workload) {
      out.skipped.no_workload++
      continue
    }
    const ref = parseRepoUrl(workload.fields.repo, gitlabHost)
    if (!ref) {
      out.skipped.no_repo_url++
      continue
    }
    const repoId = resolve(ref)
    if (!repoId) {
      out.skipped.repo_not_tracked++
      continue
    }
    out.records.push({
      key: `${repoId}/orrery/${d.key}`,
      repoId,
      service: ref.path,
      source: 'orrery',
      environment: app.relations.environment?.[0]?.key ?? 'production',
      status,
      revision: d.fields.revision,
      revisionElsewhere: !sameRepo(parseRepoUrl(app.fields.repo_url, gitlabHost), ref),
      startedAt: d.fields.started_at ?? null,
      finishedAt,
      url: null,
      baseRevision: null,
      commits: null,
    })
  }
  return out
}

export class OrreryReader {
  private readonly url: string
  private readonly token: string
  private readonly config: Config
  private readonly store: Store
  private readonly outcomes: Outcomes
  private readonly log: Logger
  private timer: NodeJS.Timeout | null = null
  private catalog: OrreryWorkload[] = []

  constructor(config: Config, store: Store, outcomes: Outcomes, log: Logger) {
    this.url = (config.ORRERY_URL ?? '').replace(/\/$/, '')
    this.token = config.ORRERY_READ_TOKEN ?? ''
    this.config = config
    this.store = store
    this.outcomes = outcomes
    this.log = log
  }

  enabled(): boolean {
    return !!this.url && !!this.token
  }

  start(): void {
    if (!this.enabled()) {
      this.log.info('orrery deployments off: ORRERY_URL and ORRERY_READ_TOKEN are not both set')
      return
    }
    this.outcomes.orreryOn = true
    this.log.info('orrery deployments on', { url: this.url })
    // The first sweep needs a head start, so the repositories the syncs name are known.
    setTimeout(() => void this.read('startup'), 60_000).unref()
    this.timer = setInterval(() => void this.read('interval'), this.config.SYNC_INTERVAL_MINUTES * 60_000)
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer)
  }

  /** The catalog's workloads as of the last read, empty before the first. */
  workloads(): OrreryWorkload[] {
    return this.catalog
  }

  private async get<T>(path: string): Promise<T> {
    const res = await fetch(this.url + path, { headers: { authorization: `Bearer ${this.token}`, accept: 'application/json' }, signal: AbortSignal.timeout(60_000) })
    const text = await res.text()
    if (!res.ok) throw new Error(`orrery GET ${path}: ${res.status} ${text.slice(0, 300)}`)
    return JSON.parse(text) as T
  }

  private async entities<T>(blueprint: string, query = ''): Promise<T[]> {
    const all: T[] = []
    for (let offset = 0; ; offset += PAGE) {
      const page = await this.get<{ entities: T[]; total: number }>(`/api/entities?blueprint=${blueprint}&limit=${PAGE}&offset=${offset}${query}`)
      all.push(...page.entities)
      if (all.length >= page.total || page.entities.length === 0) return all
    }
  }

  /** One pass: every sync within the kept window, mapped and kept, then the commits of the repositories it touched. */
  async read(reason: string): Promise<void> {
    const started = Date.now()
    try {
      const since = new Date(started - KEEP_DAYS * DAY_MS).toISOString()
      const filter = encodeURIComponent(JSON.stringify({ field: 'deployed_at', op: 'gte', value: since }))
      const [deployments, apps, workloads] = await Promise.all([
        this.entities<OrreryDeployment>('deployment', `&filter=${filter}&sort=-deployed_at`),
        this.entities<OrreryApp>('argocd_app'),
        this.entities<OrreryWorkload>('workload'),
      ])
      this.catalog = workloads
      const gitlabHost = new URL(this.config.GITLAB_URL).hostname
      const mapped = mapOrreryDeployments(deployments, apps, workloads, (ref) => this.store.findRepoByFullName(ref.provider, ref.fullName)?.id ?? null, gitlabHost)
      const fresh = this.outcomes.keep(mapped.records)
      this.log.info('orrery deployments read', { reason, syncs: deployments.length, kept: mapped.records.length, fresh, skipped: mapped.skipped, ms: Date.now() - started })
      for (const repoId of new Set(mapped.records.map((r) => r.repoId))) await this.outcomes.readCommits(repoId)
    } catch (error) {
      this.log.error('orrery deployments read failed', { reason, error })
    }
  }
}
