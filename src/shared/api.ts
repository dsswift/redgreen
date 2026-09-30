// Wire types between the server and the web app.

import type { Account, ProviderKind, Release, RepoSnapshot } from './model.ts'
import type { Level, QuietReason, RepoHealth, RepoSettings, RuleSeverities, Signal } from './rules.ts'

export interface RateLimit {
  scope: string
  remaining: number
  limit: number
  resetAt: string
}

export interface ProviderStatus {
  kind: ProviderKind
  configured: boolean
  source: string | null
  links: { label: string; url: string }[]
  rateLimits: RateLimit[]
}

export interface SyncStatus {
  sweeping: boolean
  lastSweepStartedAt: string | null
  lastSweepFinishedAt: string | null
  queued: number
  inFlight: number
  lastError: string | null
}

/** One repo as the board shows it. */
export interface RepoCard {
  id: string
  provider: ProviderKind
  accountId: string
  name: string
  fullName: string
  url: string | null
  level: Level
  quietReason: QuietReason | null
  signals: Signal[]
  running: boolean
  successRate: number | null
  lastRunAt: string | null
  pushedAt: string | null
  pipelines: number
  openIssues: number
  openPullRequests: number
  stars: number
  language: string | null
  archived: boolean
  fork: boolean
  visibility: RepoSnapshot['visibility'] | null
  latestRelease: Release | null
  syncedAt: string | null
  syncError: string | null
  muted: boolean
  /** When the repo entered its current level, per the recorded history. */
  levelSince: string | null
}

export type LevelCounts = Record<Level, number>

export interface AccountSummary extends Account {
  counts: LevelCounts
  repos: number
}

export interface EstateTotals extends LevelCounts {
  repos: number
  pipelines: number
  runsLast24h: number
  releasesLast7d: number
  openPullRequests: number
  openIssues: number
}

export interface EstateResponse {
  generatedAt: string
  providers: ProviderStatus[]
  accounts: AccountSummary[]
  repos: RepoCard[]
  totals: EstateTotals
  sync: SyncStatus
}

export interface LevelChange {
  level: Level
  at: string
}

export interface RepoDetailResponse {
  id: string
  fullName: string
  account: Account
  snapshot: RepoSnapshot | null
  health: RepoHealth | null
  settings: RepoSettings
  history: LevelChange[]
  syncedAt: string | null
  syncError: string | null
}

export interface SettingsResponse {
  rules: RuleSeverities
  defaults: RuleSeverities
  providers: ProviderStatus[]
  accounts: Account[]
  publicUrl: string
  /** Suggested app name for the GitHub setup form. */
  githubAppName: string
}

export type ServerEvent =
  | { type: 'repo'; id: string }
  | { type: 'estate' }
  | { type: 'sync'; status: SyncStatus }
