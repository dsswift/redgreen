// Shapes of the GitLab REST and GraphQL responses redgreen reads, and their
// mapping onto the estate model. Only the fields used are declared.

import type { AlertCounts, ChecksState, Pipeline, PipelineState, Run, RunConclusion, RunStatus, RunTrigger, Schedule } from '../../../shared/model.ts'
import { RAILS_TIME_ZONES } from './timezones.ts'

export interface GlNamespace {
  id: number
  name: string
  path: string
  full_path: string
  kind: 'user' | 'group'
  parent_id: number | null
  avatar_url: string | null
  web_url: string
}

export interface GlGroup {
  id: number
  name: string
  path: string
  full_path: string
  parent_id: number | null
  avatar_url: string | null
  web_url: string
}

export interface GlUser {
  id: number
  username: string
  name: string
  avatar_url: string | null
  web_url: string
}

export interface GlProject {
  id: number
  name: string
  path_with_namespace: string
  web_url: string
  description: string | null
  default_branch: string | null
  visibility: 'public' | 'private' | 'internal'
  archived: boolean
  forked_from_project?: { id: number } | null
  star_count: number
  open_issues_count?: number
  last_activity_at: string
  created_at: string
  /** Only present for members. */
  builds_access_level?: 'disabled' | 'private' | 'enabled'
  ci_config_path?: string | null
  namespace: GlNamespace
}

export interface GlPipeline {
  id: number
  iid: number
  name: string | null
  ref: string
  sha: string
  status: string
  source: string
  created_at: string
  updated_at: string
  web_url: string
}

export interface GlSchedule {
  id: number
  description: string
  ref: string
  cron: string
  cron_timezone: string
  active: boolean
  created_at: string
  updated_at: string
}

export interface GlRelease {
  tag_name: string
  name: string | null
  released_at: string
  upcoming_release: boolean
  _links: { self: string }
}

export interface GlMergeRequest {
  iid: number
  title: string
  web_url: string
  draft: boolean
  updated_at: string
  author: { username: string } | null
}

export interface GlSecurity {
  securityScanners: { enabled: string[] } | null
  dependency: { critical: number; high: number } | null
  code: { critical: number; high: number } | null
}

/** Pipeline states GitLab reports as settled. Anything else is still moving. */
const FINISHED: Readonly<Record<string, RunConclusion>> = {
  success: 'success',
  failed: 'failure',
  canceled: 'cancelled',
  skipped: 'skipped',
  /** Blocked on a manual job: says nothing about health. */
  manual: 'action_required',
}

export function runStatus(status: string): { status: RunStatus; conclusion: RunConclusion | null } {
  const conclusion = FINISHED[status]
  if (conclusion) return { status: 'completed', conclusion }
  return { status: status === 'running' || status === 'canceling' ? 'running' : 'queued', conclusion: null }
}

export function trigger(pipeline: GlPipeline, tags: ReadonlySet<string>): RunTrigger {
  switch (pipeline.source) {
    case 'push':
      return tags.has(pipeline.ref) ? 'tag' : 'push'
    case 'merge_request_event':
    case 'external_pull_request_event':
      return 'pull_request'
    case 'schedule':
    case 'scheduled':
      return 'schedule'
    case 'web':
    case 'chat':
      return 'manual'
    default:
      return 'other'
  }
}

export function toRun(pipeline: GlPipeline, tags: ReadonlySet<string>): Run {
  return {
    id: String(pipeline.id),
    number: pipeline.iid,
    title: pipeline.name ?? pipeline.ref,
    trigger: trigger(pipeline, tags),
    ref: pipeline.ref,
    sha: pipeline.sha,
    ...runStatus(pipeline.status),
    createdAt: pipeline.created_at,
    updatedAt: pipeline.updated_at,
    url: pipeline.web_url,
    actor: null,
  }
}

/** The one pipeline a project's CI configuration defines. Runs from schedules are reported on their own pipelines. */
export function toMainPipeline(project: GlProject, state: PipelineState, runs: Run[]): Pipeline {
  return {
    id: 'ci',
    name: 'Pipeline',
    path: ciConfigPath(project),
    url: `${project.web_url}/-/pipelines`,
    state,
    createdAt: project.created_at,
    updatedAt: runs[0]?.updatedAt ?? project.last_activity_at,
    schedules: [],
    runs,
  }
}

export function toSchedulePipeline(project: GlProject, schedule: GlSchedule, ciDisabled: boolean, runs: Run[]): Pipeline {
  return {
    id: `schedule:${schedule.id}`,
    name: schedule.description,
    path: ciConfigPath(project),
    url: `${project.web_url}/-/pipeline_schedules`,
    state: ciDisabled || !schedule.active ? 'disabled' : 'enabled',
    createdAt: schedule.created_at,
    updatedAt: schedule.updated_at,
    schedules: [toSchedule(schedule)],
    runs,
  }
}

export function ciConfigPath(project: GlProject): string {
  return project.ci_config_path || '.gitlab-ci.yml'
}

/** Resolves GitLab's stored time zone name to an IANA identifier; unknown names fall back to UTC. */
export function toSchedule(schedule: GlSchedule): Schedule {
  return { cron: schedule.cron.trim(), timezone: ianaTimeZone(schedule.cron_timezone) }
}

export function ianaTimeZone(name: string): string {
  for (const candidate of [name, RAILS_TIME_ZONES[name]]) {
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

/** The state of a merge request's head pipeline as a checks state. */
export function checksState(status: string | null | undefined): ChecksState {
  switch (status) {
    case null:
    case undefined:
    case 'canceled':
    case 'skipped':
      return 'none'
    case 'success':
      return 'success'
    case 'failed':
      return 'failure'
    default:
      return 'pending'
  }
}

const DEPENDENCY_SCANNERS = ['DEPENDENCY_SCANNING', 'CONTAINER_SCANNING']
const CODE_SCANNERS = ['SAST', 'SAST_ADVANCED', 'SAST_IAC', 'SECRET_DETECTION', 'DAST', 'API_FUZZING', 'COVERAGE_FUZZING']

/** Folds the security query into the two feeds the model knows. Null when no scanner of that kind runs, or the tier hides counts. */
export function securityCounts(security: GlSecurity | null, projectUrl: string): { dependabot: AlertCounts | null; codeScanning: AlertCounts | null } {
  const url = `${projectUrl}/-/security/vulnerability_report`
  const enabled = new Set(security?.securityScanners?.enabled ?? [])
  const counts = (scanners: string[], found: { critical: number; high: number } | null) =>
    found && scanners.some((s) => enabled.has(s)) ? { critical: found.critical, high: found.high, url } : null
  return {
    dependabot: counts(DEPENDENCY_SCANNERS, security?.dependency ?? null),
    codeScanning: counts(CODE_SCANNERS, security?.code ?? null),
  }
}

/** Dominant language by share of the repository, or null for an empty one. */
export function topLanguage(languages: Record<string, number>): string | null {
  const [top] = Object.entries(languages).sort((a, b) => b[1] - a[1])
  return top ? top[0] : null
}
