// Shapes of the Azure DevOps REST responses redgreen reads, and their mapping
// onto the estate model. Only the fields used are declared.

import type { AlertCounts, ChecksState, Pipeline, PipelineState, RepoFacts, RepoSnapshot, Run, RunConclusion, RunStatus, RunTrigger, Schedule } from '../../../shared/model.ts'
import type { FileSchedules } from './pipeline-file.ts'
import { WINDOWS_TIME_ZONES } from './timezones.ts'

export interface AdoRepo {
  id: string
  name: string
  webUrl: string
  /** A full ref such as `refs/heads/main`. Absent on a repository with no commits. */
  defaultBranch?: string
  isDisabled?: boolean
  isFork?: boolean
  project: { id: string; name: string; visibility?: string }
}

/** A schedule set in the pipeline editor rather than in the YAML file. */
export interface AdoDefinitionSchedule {
  timeZoneId: string
  startHours: number
  startMinutes: number
  /** A bit per weekday, or the weekday names when the service writes the flags out. */
  daysToBuild: number | string
  scheduleOnlyWithChanges?: boolean
}

export interface AdoDefinition {
  id: number
  name: string
  queueStatus?: 'enabled' | 'paused' | 'disabled'
  quality?: 'definition' | 'draft'
  /** When this revision of the definition was saved. */
  createdDate: string
  /** Type 2 is a YAML pipeline; type 1 is one built in the editor. */
  process?: { type: number; yamlFilename?: string }
  triggers?: { triggerType: string; schedules?: AdoDefinitionSchedule[] }[]
  _links?: { web?: { href: string } }
}

export interface AdoBuild {
  id: number
  buildNumber: string
  status: string
  result?: string
  queueTime: string
  startTime?: string
  finishTime?: string
  definition: { id: number }
  sourceBranch: string
  sourceVersion: string
  reason: string
  requestedFor?: { displayName?: string }
  _links?: { web?: { href: string } }
}

export interface AdoPullRequest {
  pullRequestId: number
  title: string
  isDraft?: boolean
  creationDate: string
  createdBy?: { displayName?: string }
}

export interface AdoPolicy {
  isEnabled: boolean
  isBlocking: boolean
  isDeleted?: boolean
  type: { id: string }
  settings?: { minimumApproverCount?: number }
}

export interface AdoAlert {
  alertType: string
  severity: string
}

export interface AdoLanguageMetrics {
  repositoryLanguageAnalytics?: { id: string; languageBreakdown?: { name: string; languagePercentage?: number }[] }[]
}

export interface AdoCommit {
  author?: { name?: string; date?: string }
}

/** Policy types every organization shares. */
const MINIMUM_REVIEWERS_POLICY = 'fa4e907d-c16b-4a4c-9dfa-4906e5d171dd'
const REQUIRED_REVIEWERS_POLICY = 'fd2167ab-b0be-447a-8ec8-39368250530e'

/** `refs/heads/main` as `main`, `refs/tags/v1` as `v1`. Other refs keep everything after `refs/`. */
export function shortRef(ref: string): string {
  return ref.replace(/^refs\/(heads\/|tags\/)?/, '')
}

export function visibility(repo: AdoRepo): RepoSnapshot['visibility'] {
  return repo.project.visibility === 'public' ? 'public' : 'private'
}

/** Results Azure Pipelines reports for a finished build. "Partially succeeded" finished with a tolerated step failure. */
const FINISHED: Readonly<Record<string, RunConclusion>> = {
  succeeded: 'success',
  partiallySucceeded: 'success',
  failed: 'failure',
  canceled: 'cancelled',
}

export function runStatus(build: Pick<AdoBuild, 'status' | 'result'>): { status: RunStatus; conclusion: RunConclusion | null } {
  if (build.status === 'completed') return { status: 'completed', conclusion: FINISHED[build.result ?? ''] ?? 'neutral' }
  return { status: build.status === 'inProgress' || build.status === 'cancelling' ? 'running' : 'queued', conclusion: null }
}

export function trigger(build: Pick<AdoBuild, 'reason' | 'sourceBranch'>): RunTrigger {
  switch (build.reason) {
    case 'individualCI':
    case 'batchedCI':
      return build.sourceBranch.startsWith('refs/tags/') ? 'tag' : 'push'
    case 'pullRequest':
      return 'pull_request'
    case 'schedule':
      return 'schedule'
    case 'manual':
    case 'userCreated':
      return 'manual'
    default:
      return 'other'
  }
}

export function toRun(build: AdoBuild): Run {
  return {
    id: String(build.id),
    number: build.id,
    title: build.buildNumber,
    trigger: trigger(build),
    ref: shortRef(build.sourceBranch),
    sha: build.sourceVersion,
    ...runStatus(build),
    createdAt: build.queueTime,
    updatedAt: build.finishTime ?? build.startTime ?? build.queueTime,
    url: build._links?.web?.href ?? '',
    actor: build.requestedFor?.displayName ?? null,
  }
}

/** A paused pipeline queues runs without starting them; like a disabled one, it reports nothing new. */
export function pipelineState(definition: AdoDefinition): PipelineState {
  return definition.queueStatus === 'paused' || definition.queueStatus === 'disabled' ? 'disabled' : 'enabled'
}

export function toPipeline(definition: AdoDefinition, projectUrl: string, schedules: Schedule[], runs: Run[]): Pipeline {
  return {
    id: String(definition.id),
    name: definition.name,
    path: definition.process?.yamlFilename ?? 'classic pipeline',
    url: definition._links?.web?.href ?? `${projectUrl}/_build?definitionId=${definition.id}`,
    state: pipelineState(definition),
    createdAt: definition.createdDate,
    updatedAt: definition.createdDate,
    schedules,
    runs,
  }
}

/** Schedules set in the pipeline editor, or null when the definition has none and the YAML file decides. */
export function definitionSchedules(definition: AdoDefinition): FileSchedules | null {
  const all = (definition.triggers ?? []).filter((t) => t.triggerType === 'schedule').flatMap((t) => t.schedules ?? [])
  if (all.length === 0) return null
  const always = all.filter((s) => !s.scheduleOnlyWithChanges)
  return { schedules: always.map(toSchedule), conditional: all.length - always.length }
}

export function toSchedule(schedule: AdoDefinitionSchedule): Schedule {
  return { cron: `${schedule.startMinutes} ${schedule.startHours} * * ${cronDays(schedule.daysToBuild)}`, timezone: ianaTimeZone(schedule.timeZoneId) }
}

/** Weekdays in the order of their bits, lowest first, with the number cron knows each by. */
const WEEKDAYS: readonly (readonly [string, number])[] = [
  ['monday', 1],
  ['tuesday', 2],
  ['wednesday', 3],
  ['thursday', 4],
  ['friday', 5],
  ['saturday', 6],
  ['sunday', 0],
]

/** The day-of-week field of a cron expression for a set of weekdays. */
export function cronDays(days: number | string): string {
  const chosen = WEEKDAYS.filter(([name], bit) => {
    if (typeof days === 'number') return (days & (1 << bit)) !== 0
    const named = days.toLowerCase().split(',').map((d) => d.trim())
    return named.includes(name) || named.includes('all')
  })
  return chosen.length === 0 || chosen.length === WEEKDAYS.length ? '*' : chosen.map(([, cron]) => cron).join(',')
}

/** Resolves a Windows time zone name to an IANA identifier; unknown names fall back to UTC. */
export function ianaTimeZone(name: string): string {
  for (const candidate of [WINDOWS_TIME_ZONES[name], name]) {
    if (candidate && isTimeZone(candidate)) return candidate
  }
  return 'UTC'
}

function isTimeZone(name: string): boolean {
  try {
    new Intl.DateTimeFormat('en', { timeZone: name })
    return true
  } catch {
    return false
  }
}

/** A pull request's checks, from the builds of its merge ref: the newest build of each pipeline decides. */
export function checksState(builds: AdoBuild[]): ChecksState {
  const latest = new Map<number, AdoBuild>()
  for (const build of [...builds].sort((a, b) => b.queueTime.localeCompare(a.queueTime))) {
    if (!latest.has(build.definition.id)) latest.set(build.definition.id, build)
  }
  const states = [...latest.values()].map(runStatus)
  if (states.some((s) => s.conclusion === 'failure')) return 'failure'
  if (states.some((s) => s.status !== 'completed')) return 'pending'
  return states.some((s) => s.conclusion === 'success') ? 'success' : 'none'
}

const DEPENDENCY_ALERTS = ['dependency', 'malware']

/** Folds Advanced Security alerts into the two feeds the model knows. Secrets count with code. */
export function securityCounts(alerts: AdoAlert[], repoUrl: string): { dependabot: AlertCounts; codeScanning: AlertCounts } {
  const url = `${repoUrl}/alerts`
  const count = (dependency: boolean, severity: string) => alerts.filter((a) => DEPENDENCY_ALERTS.includes(a.alertType) === dependency && a.severity === severity).length
  return {
    dependabot: { critical: count(true, 'critical'), high: count(true, 'high'), url },
    codeScanning: { critical: count(false, 'critical'), high: count(false, 'high'), url },
  }
}

/** Dominant programming language per repository id. Entries without a language share are data or binary files. */
export function topLanguages(metrics: AdoLanguageMetrics): Map<string, string | null> {
  return new Map(
    (metrics.repositoryLanguageAnalytics ?? []).map((repo) => {
      const [top] = (repo.languageBreakdown ?? []).filter((l) => l.languagePercentage !== undefined).sort((a, b) => (b.languagePercentage ?? 0) - (a.languagePercentage ?? 0))
      return [repo.id, top?.name ?? null]
    }),
  )
}

/** Branch rules from the policies on the default branch. Null inputs are parts the application may not read. */
export function toFacts(policies: AdoPolicy[] | null, rootPaths: string[] | null, commit: AdoCommit | null): RepoFacts {
  const enabled = policies?.filter((p) => p.isEnabled && !p.isDeleted) ?? null
  const reviewers = enabled?.filter((p) => p.type.id === MINIMUM_REVIEWERS_POLICY) ?? null
  const names = rootPaths?.map((p) => p.replace(/^\//, '').toLowerCase()) ?? null
  return {
    topics: [],
    branchProtected: enabled ? enabled.some((p) => p.isBlocking) : null,
    requiresReview: reviewers ? reviewers.length > 0 : null,
    requiredApprovals: reviewers && reviewers.length > 0 ? Math.max(...reviewers.map((p) => p.settings?.minimumApproverCount ?? 0)) : null,
    // The nearest thing to code owners: reviewers a policy adds by path and requires.
    requiresCodeOwnerReview: enabled ? enabled.some((p) => p.type.id === REQUIRED_REVIEWERS_POLICY && p.isBlocking) : null,
    hasCodeowners: null,
    hasReadme: names ? names.some((n) => n.startsWith('readme')) : null,
    hasDockerfile: names ? names.some((n) => n === 'dockerfile' || n.endsWith('.dockerfile')) : null,
    lastCommitter: commit?.author?.name ?? null,
    lastCommitAt: commit?.author?.date ?? null,
  }
}
