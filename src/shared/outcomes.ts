// Outcomes: the four DORA measures, computed at read time from stored
// deployments and the commits each one carried. Nothing here reads a forge
// or a store; the server hands in records and gets numbers back, so the
// same records always give the same numbers and a rule change never needs a
// resync. The web app imports the same bands to grade what it shows.

export const WINDOWS = [7, 30, 90] as const
export type WindowDays = (typeof WINDOWS)[number]

export type DeploymentSource = 'forge' | 'orrery'

/** One commit a deployment carried: enough to say how long it waited. */
export interface CarriedCommit {
  sha: string
  authoredAt: string
  /** The first line of the message. */
  title: string
}

/** One rollout that reached an end, as the store keeps it. */
export interface DeploymentRecord {
  /** Unique across the estate: the repository, the source, and the source's own id. */
  key: string
  repoId: string
  /** The folder of the service it rolled out, `''` for the whole repository. */
  service: string
  source: DeploymentSource
  environment: string
  status: 'succeeded' | 'failed'
  revision: string
  /** True when the revision is a commit in another repository, such as a deployment manifest; the repository's release tags stand in for it. */
  revisionElsewhere: boolean
  startedAt: string | null
  finishedAt: string
  url: string | null
  /** The revision the commits were counted from, null when this was the first known deployment. */
  baseRevision: string | null
  /** The commits it carried, oldest first. Null until they are read. */
  commits: CarriedCommit[] | null
}

/** Something that went wrong in production and was noticed, from an incident source; see `server/outcomes/incidents.ts`. */
export interface Incident {
  repoId: string
  service: string
  openedAt: string
  resolvedAt: string | null
}

export type Band = 'elite' | 'high' | 'medium' | 'low'
export const BANDS: Band[] = ['elite', 'high', 'medium', 'low']

export interface Measures {
  /** Deployments that reached an end in the window, failed ones included. */
  deployments: number
  /** Successful deployments per day over the window. */
  frequencyPerDay: number
  /** Hours from a commit's authoring to the end of the deployment that carried it. Null when no carried commit is known. */
  leadTimeP50Hours: number | null
  leadTimeP90Hours: number | null
  /** The share of deployments that failed, 0 to 1. Null when nothing deployed. */
  changeFailureRate: number | null
  /** Hours from a failure to the next successful deployment or the incident's end. Null when nothing failed or nothing was restored. */
  timeToRestoreP50Hours: number | null
  /** The worst band among the measures with data. Null when nothing deployed in the window. */
  grade: Band | null
}

/** The measures per bucket across the window, oldest first, for a sparkline. */
export interface Series {
  frequency: number[]
  leadTime: (number | null)[]
  changeFailureRate: (number | null)[]
  timeToRestore: (number | null)[]
}

const HOUR_MS = 3_600_000
const DAY_MS = 24 * HOUR_MS
/** A deployment this soon after another, carrying only fixes, is the other's failure. */
export const FIX_FOLLOW_MS = DAY_MS
/** Days per sparkline bucket for each window. */
const BUCKET_DAYS: Record<WindowDays, number> = { 7: 1, 30: 3, 90: 7 }

/** A commit that only repairs: a conventional `fix` or `revert`, or a forge's revert. A merge commit says nothing either way. */
export function isRepair(title: string): boolean | null {
  if (/^merge /i.test(title)) return null
  return /^(fix|revert)(\(.*?\))?!?:/i.test(title) || /^revert\b/i.test(title)
}

export interface ClassifiedDeployment extends DeploymentRecord {
  /** True when it failed outright, was followed within a day by a deployment of only repairs, or was followed by an incident. */
  failed: boolean
  /** When the failure ended: the next successful deployment, the repair, or the incident's end. Null while unrestored. */
  restoredAt: string | null
  /** Why it counts as failed, for the page. */
  failure: 'status' | 'repair' | 'incident' | null
}

/** Marks each deployment failed or not, with when it was restored, from the stream it belongs to: one repository, one service, one environment. */
export function classify(records: DeploymentRecord[], incidents: Incident[] = []): ClassifiedDeployment[] {
  const streams = new Map<string, DeploymentRecord[]>()
  for (const r of records) {
    const k = `${r.repoId}\n${r.service}\n${r.environment}`
    const list = streams.get(k) ?? []
    list.push(r)
    streams.set(k, list)
  }
  const out: ClassifiedDeployment[] = []
  for (const list of streams.values()) {
    list.sort((a, b) => a.finishedAt.localeCompare(b.finishedAt))
    list.forEach((r, i) => {
      const at = Date.parse(r.finishedAt)
      const later = list.slice(i + 1)
      const nextSuccess = later.find((d) => d.status === 'succeeded')
      if (r.status === 'failed') {
        out.push({ ...r, failed: true, failure: 'status', restoredAt: nextSuccess?.finishedAt ?? null })
        return
      }
      const repair = later.find((d) => {
        if (d.status !== 'succeeded' || Date.parse(d.finishedAt) - at > FIX_FOLLOW_MS) return false
        const judged = (d.commits ?? []).map((c) => isRepair(c.title)).filter((x): x is boolean => x !== null)
        return judged.length > 0 && judged.every(Boolean)
      })
      if (repair) {
        out.push({ ...r, failed: true, failure: 'repair', restoredAt: repair.finishedAt })
        return
      }
      const incident = incidents.find((x) => x.repoId === r.repoId && x.service === r.service && Date.parse(x.openedAt) >= at && Date.parse(x.openedAt) - at <= FIX_FOLLOW_MS)
      if (incident) {
        out.push({ ...r, failed: true, failure: 'incident', restoredAt: incident.resolvedAt })
        return
      }
      out.push({ ...r, failed: false, failure: null, restoredAt: null })
    })
  }
  return out
}

/** The p-th percentile by nearest rank, null for an empty list. */
export function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null
  const sorted = [...values].sort((a, b) => a - b)
  const rank = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1))
  return sorted[rank] ?? null
}

const leadTimes = (d: ClassifiedDeployment): number[] => {
  const end = Date.parse(d.finishedAt)
  return (d.commits ?? []).map((c) => (end - Date.parse(c.authoredAt)) / HOUR_MS).filter((h) => Number.isFinite(h) && h >= 0)
}

const restoreHours = (d: ClassifiedDeployment): number | null => (d.failed && d.restoredAt ? (Date.parse(d.restoredAt) - Date.parse(d.finishedAt)) / HOUR_MS : null)

/** The four measures over deployments that ended in [from, to). */
export function measure(classified: ClassifiedDeployment[], from: number, to: number): Measures {
  const inside = classified.filter((d) => {
    const at = Date.parse(d.finishedAt)
    return at >= from && at < to
  })
  const days = (to - from) / DAY_MS
  const succeeded = inside.filter((d) => d.status === 'succeeded').length
  const failed = inside.filter((d) => d.failed).length
  const lead = inside.filter((d) => d.status === 'succeeded').flatMap(leadTimes)
  const restores = inside.map(restoreHours).filter((h): h is number => h !== null && h >= 0)
  const m: Omit<Measures, 'grade'> = {
    deployments: inside.length,
    frequencyPerDay: days > 0 ? succeeded / days : 0,
    leadTimeP50Hours: percentile(lead, 50),
    leadTimeP90Hours: percentile(lead, 90),
    changeFailureRate: inside.length > 0 ? failed / inside.length : null,
    timeToRestoreP50Hours: percentile(restores, 50),
  }
  return { ...m, grade: grade(m) }
}

/** The per-bucket series across [from, to), oldest bucket first. */
export function series(classified: ClassifiedDeployment[], from: number, to: number, window: WindowDays): Series {
  const step = BUCKET_DAYS[window] * DAY_MS
  const buckets = Math.ceil((to - from) / step)
  const out: Series = { frequency: [], leadTime: [], changeFailureRate: [], timeToRestore: [] }
  for (let i = buckets - 1; i >= 0; i--) {
    const m = measure(classified, Math.max(from, to - (i + 1) * step), to - i * step)
    out.frequency.push(Math.round(m.frequencyPerDay * BUCKET_DAYS[window] * 100) / 100)
    out.leadTime.push(m.leadTimeP50Hours)
    out.changeFailureRate.push(m.changeFailureRate)
    out.timeToRestore.push(m.timeToRestoreP50Hours)
  }
  return out
}

// DORA bands --------------------------------------------------------------------

export const frequencyBand = (perDay: number): Band => (perDay >= 1 ? 'elite' : perDay >= 1 / 7 ? 'high' : perDay >= 1 / 30 ? 'medium' : 'low')
export const leadTimeBand = (hours: number): Band => (hours < 24 ? 'elite' : hours < 7 * 24 ? 'high' : hours < 30 * 24 ? 'medium' : 'low')
export const failureRateBand = (rate: number): Band => (rate <= 0.05 ? 'elite' : rate <= 0.1 ? 'high' : rate <= 0.15 ? 'medium' : 'low')
export const restoreBand = (hours: number): Band => (hours < 1 ? 'elite' : hours < 24 ? 'high' : hours < 7 * 24 ? 'medium' : 'low')

/** The worst band among the measures that have data. Null when nothing deployed. */
export function grade(m: Omit<Measures, 'grade'>): Band | null {
  if (m.deployments === 0) return null
  const bands: Band[] = [frequencyBand(m.frequencyPerDay)]
  if (m.leadTimeP50Hours !== null) bands.push(leadTimeBand(m.leadTimeP50Hours))
  if (m.changeFailureRate !== null) bands.push(failureRateBand(m.changeFailureRate))
  if (m.timeToRestoreP50Hours !== null) bands.push(restoreBand(m.timeToRestoreP50Hours))
  return bands.reduce((worst, b) => (BANDS.indexOf(b) > BANDS.indexOf(worst) ? b : worst))
}

// Rollups ---------------------------------------------------------------------

export interface OutcomeRow {
  repoId: string
  fullName: string
  /** The service's folder, `''` for the whole repository. */
  service: string
  /** The service's name as detected, or the repository's. */
  name: string
  current: Measures
  previous: Measures
  series: Series
  /** Where the deployments came from. */
  sources: DeploymentSource[]
  /** The newest deployment's end. */
  lastDeployedAt: string | null
}

export interface OutcomesSummary {
  current: Measures
  previous: Measures
}

export interface OutcomesInput {
  records: DeploymentRecord[]
  incidents?: Incident[]
  /** Names for a row: the repository's full name, and a service's name by its folder. */
  names: (repoId: string, service: string) => { fullName: string; name: string } | null
  window: WindowDays
  now: number
}

/** The estate's measures and one row per repository and service, computed from the records alone. */
export function outcomes({ records, incidents = [], names, window, now }: OutcomesInput): { estate: OutcomesSummary; rows: OutcomeRow[] } {
  const classified = classify(records, incidents)
  const to = now
  const from = now - window * DAY_MS
  const before = from - window * DAY_MS
  const groups = new Map<string, ClassifiedDeployment[]>()
  for (const d of classified) {
    const k = `${d.repoId}\n${d.service}`
    const list = groups.get(k) ?? []
    list.push(d)
    groups.set(k, list)
  }
  const rows: OutcomeRow[] = []
  for (const list of groups.values()) {
    const [first] = list
    if (!first) continue
    const named = names(first.repoId, first.service)
    if (!named) continue
    const current = measure(list, from, to)
    const previous = measure(list, before, from)
    if (current.deployments === 0 && previous.deployments === 0) continue
    rows.push({
      repoId: first.repoId,
      fullName: named.fullName,
      service: first.service,
      name: named.name,
      current,
      previous,
      series: series(list, from, to, window),
      sources: [...new Set(list.map((d) => d.source))].sort(),
      lastDeployedAt: list.map((d) => d.finishedAt).sort().at(-1) ?? null,
    })
  }
  rows.sort((a, b) => a.fullName.localeCompare(b.fullName) || a.service.localeCompare(b.service))
  return { estate: { current: measure(classified, from, to), previous: measure(classified, before, from) }, rows }
}
