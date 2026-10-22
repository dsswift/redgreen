// The provider-neutral estate model. Every provider normalizes into these
// shapes; nothing downstream of a provider knows which forge a repo lives on.

import type { Language } from './languages.ts'

export const PROVIDERS = ['github', 'gitlab', 'azuredevops'] as const
export type ProviderKind = (typeof PROVIDERS)[number]

export const PROVIDER_LABELS: Record<ProviderKind, string> = { github: 'GitHub', gitlab: 'GitLab', azuredevops: 'Azure DevOps' }

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

export interface Schedule {
  cron: string
  /** IANA time zone the cron expression is evaluated in. */
  timezone: string
}

export interface Pipeline {
  id: string
  name: string
  path: string
  url: string
  state: PipelineState
  createdAt: string
  updatedAt: string
  /** Schedules the pipeline fires on. */
  schedules: Schedule[]
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

/** One rollout the forge recorded against a named environment. */
export interface Deployment {
  id: string
  /** The environment as the forge names it: production, staging. */
  environment: string
  status: 'succeeded' | 'failed' | 'running' | 'other'
  ref: string | null
  sha: string
  createdAt: string
  /** When it reached a final status. Null while it runs. */
  finishedAt: string | null
  url: string | null
  actor: string | null
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

/** Facts about the repository itself, for catalogs that list it as a service. Null where the forge does not say. */
export interface RepoFacts {
  topics: string[]
  branchProtected: boolean | null
  requiresReview: boolean | null
  requiredApprovals: number | null
  requiresCodeOwnerReview: boolean | null
  hasCodeowners: boolean | null
  hasReadme: boolean | null
  hasDockerfile: boolean | null
  lastCommitter: string | null
  lastCommitAt: string | null
}

/** What kind of thing a service is. People may also call one `docs` or `other` in a catalog; detection never does. */
export type ServiceType = 'backend' | 'frontend' | 'library' | 'infrastructure' | 'tool'

/** One piece of software a repository builds and something runs on its own, found from the files that build it. */
export interface DetectedService {
  /** Unique within the repository and stable while the code stays put: the folder, or `''` for the root. */
  id: string
  /** The folder the service is built from, `''` for the root. */
  path: string
  /** The name its own build file gives it, else its folder's or repository's name. */
  name: string
  type: ServiceType
  language: Language | null
  /** When the newest commit on the default branch touched the service's folder. Null when the forge could not say. */
  changedAt?: string | null
  /** Who made that commit, as the forge names them. Null when the forge could not say. */
  changedBy?: string | null
  /** When the newest commit by a person, not a bot or release tool, touched the folder. Null when none in the recent history. */
  humanChangedAt?: string | null
  /** Who made that commit, as the forge names them. */
  humanChangedBy?: string | null
  /** What sits beside the service in its own folder. Absent on services read under rules older than these facts. */
  files?: ServiceFiles
}

/**
 * What one service carries in its own folder, so two services in one
 * repository can differ: one documented and one not, one in a container and
 * one not. A folder that holds other services owns only what is not theirs.
 */
export interface ServiceFiles {
  /** A README in the service's folder, or in the nearest folder above it that holds no other service. */
  readme: boolean
  /** A Dockerfile that builds the service, by the same ownership as the README. */
  dockerfile: boolean
  /** Automated tests that belong to it: inside its folder, or a test project beside it named for it. */
  tests: boolean
}

/** The services found at one commit of the default branch. */
export interface RepoServices {
  /** The commit they were read from. Null for a repository with no commits. */
  commit: string | null
  /** The version of the rules that found them. A repository read under older rules is read again. */
  rules: number
  services: DetectedService[]
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
  /** Absent on snapshots taken before facts were collected, and for forges that do not provide them yet. */
  facts?: RepoFacts | null
  /** Recent deployments, newest first. Null when the forge keeps none for the repo or will not show them; absent on older snapshots and for forges not read yet. */
  deployments?: Deployment[] | null
  /** The services the repository holds. Null when they could not be read; absent on older snapshots. */
  services?: RepoServices | null
  syncedAt: string
}
