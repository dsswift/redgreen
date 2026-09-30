// Shapes of the GitHub REST responses redgreen reads, and their mapping onto
// the estate model. Only the fields used are declared.

import type { AlertCounts, ChecksState, Pipeline, PipelineState, Run, RunConclusion, RunTrigger } from '../../../shared/model.ts'

export interface GhRepo {
  id: number
  name: string
  full_name: string
  html_url: string
  description: string | null
  default_branch: string
  visibility?: string
  private: boolean
  archived: boolean
  fork: boolean
  language: string | null
  pushed_at: string | null
  stargazers_count: number
  open_issues_count: number
  owner: { id: number; login: string }
}

export interface GhWorkflow {
  id: number
  name: string
  path: string
  state: string
  html_url: string
  created_at: string
  updated_at: string
}

export interface GhRun {
  id: number
  run_number: number
  workflow_id: number
  display_title: string
  event: string
  head_branch: string | null
  head_sha: string
  status: string | null
  conclusion: string | null
  created_at: string
  updated_at: string
  html_url: string
  actor: { login: string } | null
}

export interface GhRelease {
  tag_name: string
  name: string | null
  published_at: string | null
  html_url: string
  draft: boolean
  prerelease: boolean
}

export interface GhPull {
  number: number
  title: string
  html_url: string
  draft: boolean
  updated_at: string
  user: { login: string } | null
  head: { sha: string }
}

export interface GhCheckRun {
  status: string
  conclusion: string | null
}

export interface GhCombinedStatus {
  state: string
  total_count: number
}

export interface GhDependabotAlert {
  security_vulnerability: { severity: string }
}

export interface GhCodeScanningAlert {
  rule: { security_severity_level: string | null; severity: string | null }
}

const CONCLUSIONS: ReadonlySet<string> = new Set<RunConclusion>([
  'success',
  'failure',
  'cancelled',
  'skipped',
  'neutral',
  'timed_out',
  'action_required',
  'startup_failure',
  'stale',
])

export function pipelineState(state: string): PipelineState | null {
  switch (state) {
    case 'active':
      return 'enabled'
    case 'disabled_inactivity':
      return 'dormant'
    case 'deleted':
      return null
    default:
      return 'disabled'
  }
}

export function toPipeline(workflow: GhWorkflow, state: PipelineState, schedules: string[], runs: Run[]): Pipeline {
  return {
    id: String(workflow.id),
    name: workflow.name,
    path: workflow.path,
    url: workflow.html_url,
    state,
    createdAt: workflow.created_at,
    updatedAt: workflow.updated_at,
    schedules,
    runs,
  }
}

export function toRun(run: GhRun, tags: ReadonlySet<string>): Run {
  return {
    id: String(run.id),
    number: run.run_number,
    title: run.display_title,
    trigger: trigger(run, tags),
    ref: run.head_branch,
    sha: run.head_sha,
    status: run.status === 'completed' ? 'completed' : run.status === 'queued' || run.status === 'waiting' || run.status === 'pending' ? 'queued' : 'running',
    conclusion: run.conclusion !== null && CONCLUSIONS.has(run.conclusion) ? (run.conclusion as RunConclusion) : null,
    createdAt: run.created_at,
    updatedAt: run.updated_at,
    url: run.html_url,
    actor: run.actor?.login ?? null,
  }
}

function trigger(run: GhRun, tags: ReadonlySet<string>): RunTrigger {
  switch (run.event) {
    case 'push':
      return run.head_branch !== null && tags.has(run.head_branch) ? 'tag' : 'push'
    case 'pull_request':
    case 'pull_request_target':
      return 'pull_request'
    case 'schedule':
      return 'schedule'
    case 'release':
      return 'release'
    case 'workflow_dispatch':
      return 'manual'
    default:
      return 'other'
  }
}

export function visibility(repo: GhRepo): 'public' | 'private' | 'internal' {
  if (repo.visibility === 'internal') return 'internal'
  return repo.private ? 'private' : 'public'
}

/** Folds check runs and legacy commit statuses on one commit into a single state. */
export function checksState(checkRuns: GhCheckRun[], status: GhCombinedStatus): ChecksState {
  const failed = checkRuns.some((c) => c.conclusion === 'failure' || c.conclusion === 'timed_out' || c.conclusion === 'startup_failure')
  if (failed || status.state === 'failure' || status.state === 'error') return 'failure'
  const pending = checkRuns.some((c) => c.status !== 'completed') || (status.total_count > 0 && status.state === 'pending')
  if (pending) return 'pending'
  return checkRuns.length + status.total_count > 0 ? 'success' : 'none'
}

export function dependabotCounts(alerts: GhDependabotAlert[], url: string): AlertCounts {
  return {
    critical: alerts.filter((a) => a.security_vulnerability.severity === 'critical').length,
    high: alerts.filter((a) => a.security_vulnerability.severity === 'high').length,
    url,
  }
}

export function codeScanningCounts(alerts: GhCodeScanningAlert[], url: string): AlertCounts {
  const level = (a: GhCodeScanningAlert) => a.rule.security_severity_level ?? (a.rule.severity === 'error' ? 'high' : null)
  return {
    critical: alerts.filter((a) => level(a) === 'critical').length,
    high: alerts.filter((a) => level(a) === 'high').length,
    url,
  }
}
