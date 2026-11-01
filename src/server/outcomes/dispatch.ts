// Reads the incidents a Dispatch desk holds and ties each to the repository
// and folder of the service it was on. Dispatch names an incident's service
// and, when its namespace resolved to one, the Orrery workload; the catalog
// gives that workload its repository, the same way it does for an ArgoCD
// sync. The list has no time filter and no pages, so one read asks for the
// most it allows and the window is cut here. A failed read logs and answers
// nothing; the outcomes page never waits on it to fail.

import type { Incident } from '../../shared/outcomes.ts'
import type { Config } from '../config.ts'
import type { Logger } from '../log.ts'
import type { Store } from '../store.ts'
import type { Telemetry } from '../telemetry.ts'
import { registerIncidentSource, type IncidentSource } from './incidents.ts'
import type { OrreryWorkload } from './orrery-reader.ts'
import { parseRepoUrl, type RepoRef } from './repo-url.ts'

/** The most incidents one list call answers. */
export const DISPATCH_LIMIT = 5000
/** A read is reused this long, so a page view and a catalog push share one. */
const CACHE_MS = 60_000
const TIMEOUT_MS = 10_000

/** One incident as `GET /v1/incidents` answers it, the fields read here. */
export interface DispatchIncident {
  id: number
  service: string
  workload?: string
  severity: 'critical' | 'warning' | 'info'
  state: 'open' | 'acknowledged' | 'mitigated' | 'resolved'
  opened_at: string
  acknowledged_at?: string | null
  mitigated_at?: string | null
  resolved_at?: string | null
}

export interface MappedIncidents {
  incidents: Incident[]
  /** Why incidents were left out, by reason. */
  skipped: Record<'before' | 'info' | 'no_workload' | 'no_repo_url' | 'repo_not_tracked', number>
}

/**
 * Turns Dispatch's incidents into incidents of the repositories this board
 * tracks. An info incident is a notice, not a failure, and is left out. The
 * service is restored when the incident is mitigated, or resolved when it
 * skipped that step. Pure, so it is tested without a desk.
 */
export function mapDispatchIncidents(list: DispatchIncident[], since: string, workloads: OrreryWorkload[], resolve: (ref: RepoRef) => string | null, gitlabHost: string): MappedIncidents {
  const workloadByKey = new Map(workloads.map((w) => [w.key, w]))
  const from = Date.parse(since)
  const out: MappedIncidents = { incidents: [], skipped: { before: 0, info: 0, no_workload: 0, no_repo_url: 0, repo_not_tracked: 0 } }
  for (const i of list) {
    if (Date.parse(i.opened_at) < from) {
      out.skipped.before++
      continue
    }
    if (i.severity === 'info') {
      out.skipped.info++
      continue
    }
    const workload = workloadByKey.get(i.workload || i.service)
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
    out.incidents.push({ repoId, service: ref.path, openedAt: i.opened_at, resolvedAt: i.mitigated_at || i.resolved_at || null })
  }
  return out
}

export class DispatchIncidents implements IncidentSource {
  private readonly url: string
  private readonly token: string
  private readonly config: Config
  private readonly catalog: { workloads(): OrreryWorkload[] }
  private readonly store: Store
  private readonly telemetry: Telemetry
  private readonly log: Logger
  private cached: { at: number; list: Promise<DispatchIncident[]> } | null = null

  constructor(config: Config, catalog: { workloads(): OrreryWorkload[]; enabled(): boolean }, store: Store, telemetry: Telemetry, log: Logger) {
    this.url = (config.DISPATCH_URL ?? '').replace(/\/$/, '')
    this.token = config.DISPATCH_TOKEN ?? ''
    this.config = config
    this.catalog = catalog
    this.store = store
    this.telemetry = telemetry
    this.log = log
    if (this.url && this.token && !catalog.enabled()) this.log.warn('dispatch incidents cannot be tied to a repository: the Orrery catalog is not read without ORRERY_URL and ORRERY_READ_TOKEN')
  }

  enabled(): boolean {
    return !!this.url && !!this.token
  }

  /** Registers this source when it is configured. Logs which. */
  register(): void {
    if (!this.enabled()) {
      this.log.info('dispatch incidents off: DISPATCH_URL and DISPATCH_TOKEN are not both set')
      return
    }
    registerIncidentSource(this)
    this.log.info('dispatch incidents on', { url: this.url })
  }

  async incidents(since: string): Promise<Incident[]> {
    const list = await this.read()
    const gitlabHost = new URL(this.config.GITLAB_URL).hostname
    const mapped = mapDispatchIncidents(list, since, this.catalog.workloads(), (ref) => this.store.findRepoByFullName(ref.provider, ref.fullName)?.id ?? null, gitlabHost)
    this.log.debug('dispatch incidents mapped', { since, read: list.length, kept: mapped.incidents.length, skipped: mapped.skipped })
    return mapped.incidents
  }

  private read(): Promise<DispatchIncident[]> {
    const now = Date.now()
    if (this.cached && now - this.cached.at < CACHE_MS) return this.cached.list
    const list = this.fetch()
    this.cached = { at: now, list }
    return list
  }

  private async fetch(): Promise<DispatchIncident[]> {
    const started = Date.now()
    try {
      const res = await fetch(`${this.url}/v1/incidents?limit=${DISPATCH_LIMIT}`, { headers: { authorization: `Bearer ${this.token}`, accept: 'application/json' }, signal: AbortSignal.timeout(TIMEOUT_MS) })
      const text = await res.text()
      if (!res.ok) throw new Error(`dispatch GET /v1/incidents: ${res.status} ${text.slice(0, 300)}`)
      const list = JSON.parse(text) as DispatchIncident[]
      this.telemetry.incidentsRead('dispatch', 'ok')
      if (list.length >= DISPATCH_LIMIT) this.log.warn('dispatch incidents read hit the list limit; older incidents may be missing', { limit: DISPATCH_LIMIT })
      this.log.info('dispatch incidents read', { incidents: list.length, ms: Date.now() - started })
      return list
    } catch (error) {
      this.telemetry.incidentsRead('dispatch', 'failed')
      this.log.error('dispatch incidents read failed; outcomes count deployments alone', { error, ms: Date.now() - started })
      this.cached = null
      return []
    }
  }
}
