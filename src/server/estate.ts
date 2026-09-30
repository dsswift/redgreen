import type { AccountSummary, EstateResponse, EstateTotals, LevelCounts, RepoCard, RepoDetailResponse, SyncStatus } from '../shared/api.ts'
import type { Account } from '../shared/model.ts'
import { compareLevels, type Level, type RepoHealth, type RepoSettings } from '../shared/rules.ts'
import { evaluate } from './health/evaluate.ts'
import type { Provider } from './providers/provider.ts'
import type { RepoRow, Store } from './store.ts'

const DAY_MS = 24 * 60 * 60 * 1000

/** Reads the store and turns snapshots into what the board shows. */
export class Estate {
  private readonly store: Store
  private readonly providers: Provider[]

  constructor(store: Store, providers: Provider[]) {
    this.store = store
    this.providers = providers
  }

  healthOf(row: RepoRow, settings: RepoSettings = this.store.getRepoSettings(row.id), now = new Date()): RepoHealth | null {
    if (!row.snapshot) return null
    return evaluate({ repo: row.snapshot, rules: this.store.getRuleSeverities(), settings, now })
  }

  /** Writes a history row when a repo's level differs from the last one recorded. Returns the level. */
  reconcileLevel(row: RepoRow, now = new Date()): Level {
    const level = this.healthOf(row, undefined, now)?.level ?? 'unknown'
    const last = this.store.latestLevel(row.id)
    if (last?.level !== level) this.store.recordLevel(row.id, level, now.toISOString())
    return level
  }

  reconcileAll(now = new Date()): void {
    for (const row of this.store.listRepos()) this.reconcileLevel(row, now)
  }

  estate(sync: SyncStatus, now = new Date()): EstateResponse {
    const rules = this.store.getRuleSeverities()
    const allSettings = this.store.listRepoSettings()
    const accounts = this.store.listAccounts()
    const rows = this.store.listRepos()

    const cards: RepoCard[] = rows.map((row) => {
      const settings = allSettings.get(row.id) ?? { muted: false, mutedPipelines: [], ruleOverrides: {} }
      const health = row.snapshot ? evaluate({ repo: row.snapshot, rules, settings, now }) : null
      return toCard(row, health, settings, this.store.latestLevel(row.id)?.at ?? null)
    })
    cards.sort((a, b) => compareLevels(a.level, b.level) || a.fullName.localeCompare(b.fullName))

    const summaries: AccountSummary[] = accounts.map((account) => {
      const mine = cards.filter((c) => c.accountId === account.id)
      return { ...toAccount(account), counts: countLevels(mine), repos: mine.length }
    })

    const dayAgo = now.getTime() - DAY_MS
    const weekAgo = now.getTime() - 7 * DAY_MS
    const snapshots = rows.flatMap((r) => (r.snapshot ? [r.snapshot] : []))
    const totals: EstateTotals = {
      ...countLevels(cards),
      repos: cards.length,
      pipelines: snapshots.reduce((n, s) => n + s.pipelines.filter((p) => p.state !== 'disabled').length, 0),
      runsLast24h: snapshots.reduce(
        (n, s) => n + s.pipelines.reduce((m, p) => m + p.runs.filter((r) => Date.parse(r.createdAt) >= dayAgo).length, 0),
        0,
      ),
      releasesLast7d: snapshots.reduce((n, s) => n + s.releases.filter((r) => Date.parse(r.publishedAt) >= weekAgo).length, 0),
      openPullRequests: snapshots.reduce((n, s) => n + s.openPullRequests, 0),
      openIssues: snapshots.reduce((n, s) => n + s.openIssues, 0),
    }

    return {
      generatedAt: now.toISOString(),
      providers: this.providers.map((p) => p.status()),
      accounts: summaries,
      repos: cards,
      totals,
      sync,
    }
  }

  detail(id: string, now = new Date()): RepoDetailResponse | null {
    const row = this.store.getRepo(id)
    if (!row) return null
    const account = this.store.getAccount(row.accountId)
    if (!account) return null
    const settings = this.store.getRepoSettings(id)
    return {
      id,
      fullName: row.fullName,
      account: toAccount(account),
      snapshot: row.snapshot,
      health: this.healthOf(row, settings, now),
      settings,
      history: this.store.levelHistory(id),
      syncedAt: row.syncedAt,
      syncError: row.syncError,
    }
  }
}

function toCard(row: RepoRow, health: RepoHealth | null, settings: RepoSettings, levelSince: string | null): RepoCard {
  const s = row.snapshot
  const name = row.fullName.split('/').at(-1)
  return {
    id: row.id,
    provider: row.provider,
    accountId: row.accountId,
    name: s?.name ?? name ?? row.fullName,
    fullName: s?.fullName ?? row.fullName,
    url: s?.url ?? null,
    level: health?.level ?? 'unknown',
    quietReason: health?.quietReason ?? null,
    signals: health?.signals ?? [],
    running: health?.running ?? false,
    successRate: health?.successRate ?? null,
    lastRunAt: health?.lastRunAt ?? null,
    pushedAt: s?.pushedAt ?? null,
    pipelines: s?.pipelines.filter((p) => p.state !== 'disabled').length ?? 0,
    openIssues: s?.openIssues ?? 0,
    openPullRequests: s?.openPullRequests ?? 0,
    stars: s?.stars ?? 0,
    language: s?.language ?? null,
    archived: s?.archived ?? false,
    fork: s?.fork ?? false,
    visibility: s?.visibility ?? null,
    latestRelease: s?.releases[0] ?? null,
    syncedAt: row.syncedAt,
    syncError: row.syncError,
    muted: settings.muted,
    levelSince: earliest(levelSince, health),
  }
}

/** The recorded level change, or an older signal start when the signals reach further back. */
function earliest(recorded: string | null, health: RepoHealth | null): string | null {
  const starts = (health?.signals ?? []).flatMap((s) => (s.since ? [s.since] : []))
  return [recorded, ...starts].filter((v): v is string => v !== null).sort()[0] ?? null
}

function toAccount(row: Account): Account {
  const { id, provider, login, name, kind, avatarUrl, url, status, lastError } = row
  return { id, provider, login, name, kind, avatarUrl, url, status, lastError }
}

function countLevels(cards: RepoCard[]): LevelCounts {
  const counts: LevelCounts = { red: 0, amber: 0, green: 0, quiet: 0, unknown: 0 }
  for (const c of cards) counts[c.level] += 1
  return counts
}
