// The provider-neutral estate model. Every provider normalizes into these
// shapes; nothing downstream of a provider knows which forge a repo lives on.

export const PROVIDERS = ['github'] as const
export type ProviderKind = (typeof PROVIDERS)[number]

export type AccountKind = 'organization' | 'user'

/** Whether redgreen syncs an account. New installations wait for approval. */
export type AccountStatus = 'active' | 'pending' | 'ignored'

export interface Account {
  id: string
  provider: ProviderKind
  login: string
  name: string
  kind: AccountKind
  avatarUrl: string | null
  url: string
  status: AccountStatus
  lastError: string | null
}

export type RunTrigger = 'push' | 'pull_request' | 'schedule' | 'release' | 'tag' | 'manual' | 'other'
export type RunStatus = 'queued' | 'running' | 'completed'
export type RunConclusion =
  | 'success'
  | 'failure'
  | 'cancelled'
  | 'skipped'
  | 'neutral'
  | 'timed_out'
  | 'action_required'
  | 'startup_failure'
  | 'stale'

export interface Run {
  id: string
  number: number
  title: string
  trigger: RunTrigger
  /** Branch or tag the run was for. */
  ref: string | null
  sha: string
  status: RunStatus
  conclusion: RunConclusion | null
  createdAt: string
  updatedAt: string
  url: string
  actor: string | null
}

/** Enabled: runs normally. Disabled: switched off by a person. Dormant: switched off by the forge for inactivity. */
export type PipelineState = 'enabled' | 'disabled' | 'dormant'

export interface Pipeline {
  id: string
  name: string
  path: string
  url: string
  state: PipelineState
  createdAt: string
  updatedAt: string
  /** Cron expressions (UTC) the pipeline is scheduled on. */
  schedules: string[]
  /** Most recent runs, newest first. Bounded; see the provider for the depth. */
  runs: Run[]
}

export interface Release {
  tag: string
  name: string
  publishedAt: string
  url: string
  prerelease: boolean
}

export type ChecksState = 'success' | 'failure' | 'pending' | 'none'

export interface PullRequest {
  number: number
  title: string
  url: string
  author: string | null
  draft: boolean
  updatedAt: string
  checks: ChecksState
}

export interface AlertCounts {
  critical: number
  high: number
  url: string
}

export interface RepoSnapshot {
  id: string
  provider: ProviderKind
  accountId: string
  name: string
  fullName: string
  url: string
  description: string | null
  defaultBranch: string
  visibility: 'public' | 'private' | 'internal'
  archived: boolean
  fork: boolean
  language: string | null
  pushedAt: string | null
  stars: number
  openIssues: number
  openPullRequests: number
  pipelines: Pipeline[]
  releases: Release[]
  pullRequests: PullRequest[]
  /** null when the forge does not expose the feed, or it is switched off for the repo. */
  security: {
    dependabot: AlertCounts | null
    codeScanning: AlertCounts | null
  }
  syncedAt: string
}
