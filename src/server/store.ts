import { mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import type { Account, ProviderKind, RepoSnapshot } from '../shared/model.ts'
import { DEFAULT_REPO_SETTINGS, defaultRuleSeverities, type Level, type RepoSettings, type RuleSeverities } from '../shared/rules.ts'

const SCHEMA = `
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  login TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  avatar_url TEXT,
  url TEXT NOT NULL,
  status TEXT NOT NULL,
  connection TEXT NOT NULL,
  last_error TEXT,
  seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  full_name TEXT NOT NULL,
  snapshot TEXT,
  synced_at TEXT,
  sync_error TEXT,
  seen_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS repos_account ON repos(account_id);
CREATE TABLE IF NOT EXISTS repo_settings (
  repo_id TEXT PRIMARY KEY,
  settings TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS level_history (
  repo_id TEXT NOT NULL,
  level TEXT NOT NULL,
  at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS level_history_repo ON level_history(repo_id, at);
CREATE TABLE IF NOT EXISTS http_cache (
  key TEXT PRIMARY KEY,
  etag TEXT NOT NULL,
  body TEXT NOT NULL,
  touched_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS http_cache_touched ON http_cache(touched_at);
`

/**
 * The largest response body the HTTP cache keeps. Every query on this
 * database runs on the event loop, so a cache that grows without bound stalls
 * the server, health checks included. A body this large is almost always
 * addressed by a commit and never asked for again.
 */
export const MAX_CACHED_BODY = 256 * 1024

/**
 * Schema upgrades, applied in order once per database file. `PRAGMA
 * user_version` records how many have run. Append only.
 */
const UPGRADES: { name: string; sql: string }[] = [
  // The cache kept every body, and whole file trees piled up by the hundred.
  // It is only a cache, so it starts again empty and bounded.
  { name: 'drop unbounded http cache', sql: 'DROP TABLE IF EXISTS http_cache;' },
]

/** Provider-specific handle for reaching an account, e.g. a GitHub App installation id. */
export type Connection = { type: 'github-app'; installationId: number } | { type: 'github-token' } | { type: 'gitlab-oauth' } | { type: 'gitlab-token' } | { type: 'azuredevops-service-principal' }

export interface AccountRow extends Account {
  connection: Connection
  seenAt: string
}

export interface RepoRow {
  id: string
  provider: ProviderKind
  accountId: string
  fullName: string
  snapshot: RepoSnapshot | null
  syncedAt: string | null
  syncError: string | null
  seenAt: string
}

export interface DiscoveredRepo {
  id: string
  provider: ProviderKind
  accountId: string
  fullName: string
}

export interface LevelChange {
  level: Level
  at: string
}

export interface CachedResponse {
  etag: string
  body: string
}

export class Store {
  private readonly db: DatabaseSync
  /** The upgrades this open applied, by name. Empty when the file was already current. */
  readonly upgraded: string[]

  constructor(path: string) {
    if (path !== ':memory:') mkdirSync(dirname(path), { recursive: true })
    this.db = new DatabaseSync(path)
    this.db.exec('PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA foreign_keys = ON;')
    this.upgraded = this.upgrade()
    this.db.exec(SCHEMA)
  }

  private upgrade(): string[] {
    const { user_version: version } = this.db.prepare('PRAGMA user_version').get() as { user_version: number }
    const pending = UPGRADES.slice(version)
    if (pending.length === 0) return []
    for (const step of pending) this.db.exec(step.sql)
    this.db.exec(`PRAGMA user_version = ${UPGRADES.length}`)
    // Gives the freed pages back, so the file is no larger than what it holds.
    this.db.exec('VACUUM')
    return pending.map((step) => step.name)
  }

  close(): void {
    this.db.close()
  }

  // Settings -----------------------------------------------------------------

  getSetting<T>(key: string): T | null {
    const row = this.db.prepare('SELECT value FROM settings WHERE key = ?').get(key) as { value: string } | undefined
    return row ? (JSON.parse(row.value) as T) : null
  }

  setSetting(key: string, value: unknown): void {
    this.db
      .prepare('INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value')
      .run(key, JSON.stringify(value))
  }

  deleteSetting(key: string): void {
    this.db.prepare('DELETE FROM settings WHERE key = ?').run(key)
  }

  getRuleSeverities(): RuleSeverities {
    return { ...defaultRuleSeverities(), ...(this.getSetting<Partial<RuleSeverities>>('rules') ?? {}) }
  }

  setRuleSeverities(rules: RuleSeverities): void {
    this.setSetting('rules', rules)
  }

  // Accounts -----------------------------------------------------------------

  listAccounts(): AccountRow[] {
    return (this.db.prepare('SELECT * FROM accounts ORDER BY login').all() as unknown as AccountRecord[]).map(toAccount)
  }

  getAccount(id: string): AccountRow | null {
    const row = this.db.prepare('SELECT * FROM accounts WHERE id = ?').get(id) as AccountRecord | undefined
    return row ? toAccount(row) : null
  }

  /** Inserts or refreshes an account. Status is only set on insert; the UI owns it afterwards. */
  upsertAccount(account: Omit<AccountRow, 'seenAt' | 'lastError'>, now: string): AccountRow {
    this.db
      .prepare(
        `INSERT INTO accounts (id, provider, login, name, kind, avatar_url, url, status, connection, last_error, seen_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)
         ON CONFLICT(id) DO UPDATE SET
           login = excluded.login, name = excluded.name, kind = excluded.kind, avatar_url = excluded.avatar_url,
           url = excluded.url, connection = excluded.connection, seen_at = excluded.seen_at`,
      )
      .run(
        account.id,
        account.provider,
        account.login,
        account.name,
        account.kind,
        account.avatarUrl,
        account.url,
        account.status,
        JSON.stringify(account.connection),
        now,
      )
    return this.getAccount(account.id)!
  }

  setAccountStatus(id: string, status: Account['status']): void {
    this.db.prepare('UPDATE accounts SET status = ? WHERE id = ?').run(status, id)
  }

  setAccountError(id: string, error: string | null): void {
    this.db.prepare('UPDATE accounts SET last_error = ? WHERE id = ?').run(error, id)
  }

  /** Drops accounts (and their repos) the provider no longer reports. */
  pruneAccounts(provider: ProviderKind, seenBefore: string): string[] {
    const stale = this.db
      .prepare('SELECT id FROM accounts WHERE provider = ? AND seen_at < ?')
      .all(provider, seenBefore) as { id: string }[]
    for (const { id } of stale) {
      this.db.prepare('DELETE FROM repos WHERE account_id = ?').run(id)
      this.db.prepare('DELETE FROM accounts WHERE id = ?').run(id)
    }
    return stale.map((s) => s.id)
  }

  // Repos --------------------------------------------------------------------

  listRepos(): RepoRow[] {
    return (this.db.prepare('SELECT * FROM repos ORDER BY full_name').all() as unknown as RepoRecord[]).map(toRepo)
  }

  listRepoIds(accountId?: string): string[] {
    const rows = accountId
      ? this.db.prepare('SELECT id FROM repos WHERE account_id = ? ORDER BY full_name').all(accountId)
      : this.db.prepare('SELECT id FROM repos ORDER BY full_name').all()
    return (rows as { id: string }[]).map((r) => r.id)
  }

  getRepo(id: string): RepoRow | null {
    const row = this.db.prepare('SELECT * FROM repos WHERE id = ?').get(id) as RepoRecord | undefined
    return row ? toRepo(row) : null
  }

  findRepoByFullName(provider: ProviderKind, fullName: string): RepoRow | null {
    const row = this.db.prepare('SELECT * FROM repos WHERE provider = ? AND full_name = ? COLLATE NOCASE').get(provider, fullName) as
      | RepoRecord
      | undefined
    return row ? toRepo(row) : null
  }

  upsertDiscoveredRepo(repo: DiscoveredRepo, now: string): void {
    this.db
      .prepare(
        `INSERT INTO repos (id, provider, account_id, full_name, seen_at) VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET account_id = excluded.account_id, full_name = excluded.full_name, seen_at = excluded.seen_at`,
      )
      .run(repo.id, repo.provider, repo.accountId, repo.fullName, now)
  }

  saveSnapshot(snapshot: RepoSnapshot): void {
    this.db
      .prepare('UPDATE repos SET snapshot = ?, synced_at = ?, sync_error = NULL, full_name = ? WHERE id = ?')
      .run(JSON.stringify(snapshot), snapshot.syncedAt, snapshot.fullName, snapshot.id)
  }

  saveSyncError(id: string, error: string): void {
    this.db.prepare('UPDATE repos SET sync_error = ? WHERE id = ?').run(error, id)
  }

  /** Drops an account's repos that discovery did not report this time. */
  pruneRepos(accountId: string, seenBefore: string): string[] {
    const stale = this.db
      .prepare('SELECT id FROM repos WHERE account_id = ? AND seen_at < ?')
      .all(accountId, seenBefore) as { id: string }[]
    for (const { id } of stale) {
      this.db.prepare('DELETE FROM repos WHERE id = ?').run(id)
      this.db.prepare('DELETE FROM repo_settings WHERE repo_id = ?').run(id)
      this.db.prepare('DELETE FROM level_history WHERE repo_id = ?').run(id)
    }
    return stale.map((s) => s.id)
  }

  // Repo settings ------------------------------------------------------------

  getRepoSettings(repoId: string): RepoSettings {
    const row = this.db.prepare('SELECT settings FROM repo_settings WHERE repo_id = ?').get(repoId) as { settings: string } | undefined
    return row ? { ...DEFAULT_REPO_SETTINGS, ...(JSON.parse(row.settings) as Partial<RepoSettings>) } : { ...DEFAULT_REPO_SETTINGS }
  }

  listRepoSettings(): Map<string, RepoSettings> {
    const rows = this.db.prepare('SELECT repo_id, settings FROM repo_settings').all() as { repo_id: string; settings: string }[]
    return new Map(rows.map((r) => [r.repo_id, { ...DEFAULT_REPO_SETTINGS, ...(JSON.parse(r.settings) as Partial<RepoSettings>) }]))
  }

  setRepoSettings(repoId: string, settings: RepoSettings): void {
    this.db
      .prepare('INSERT INTO repo_settings (repo_id, settings) VALUES (?, ?) ON CONFLICT(repo_id) DO UPDATE SET settings = excluded.settings')
      .run(repoId, JSON.stringify(settings))
  }

  // Level history ------------------------------------------------------------

  latestLevel(repoId: string): LevelChange | null {
    const row = this.db.prepare('SELECT level, at FROM level_history WHERE repo_id = ? ORDER BY at DESC LIMIT 1').get(repoId) as
      | { level: Level; at: string }
      | undefined
    return row ?? null
  }

  recordLevel(repoId: string, level: Level, at: string): void {
    this.db.prepare('INSERT INTO level_history (repo_id, level, at) VALUES (?, ?, ?)').run(repoId, level, at)
  }

  levelHistory(repoId: string, limit = 50): LevelChange[] {
    return this.db
      .prepare('SELECT level, at FROM level_history WHERE repo_id = ? ORDER BY at DESC LIMIT ?')
      .all(repoId, limit) as unknown as LevelChange[]
  }

  // HTTP cache ---------------------------------------------------------------

  getCached(key: string): CachedResponse | null {
    const row = this.db.prepare('SELECT etag, body FROM http_cache WHERE key = ?').get(key) as CachedResponse | undefined
    return row ?? null
  }

  /** Keeps a response for the next conditional request. A body over `MAX_CACHED_BODY` is not kept, and drops what was kept for the key. */
  putCached(key: string, etag: string, body: string, now: string): void {
    if (body.length > MAX_CACHED_BODY) {
      this.db.prepare('DELETE FROM http_cache WHERE key = ?').run(key)
      return
    }
    this.db
      .prepare(
        `INSERT INTO http_cache (key, etag, body, touched_at) VALUES (?, ?, ?, ?)
         ON CONFLICT(key) DO UPDATE SET etag = excluded.etag, body = excluded.body, touched_at = excluded.touched_at`,
      )
      .run(key, etag, body, now)
  }

  touchCached(key: string, now: string): void {
    this.db.prepare('UPDATE http_cache SET touched_at = ? WHERE key = ?').run(now, key)
  }

  pruneCache(touchedBefore: string): number {
    return Number(this.db.prepare('DELETE FROM http_cache WHERE touched_at < ?').run(touchedBefore).changes)
  }
}

interface AccountRecord {
  id: string
  provider: ProviderKind
  login: string
  name: string
  kind: Account['kind']
  avatar_url: string | null
  url: string
  status: Account['status']
  connection: string
  last_error: string | null
  seen_at: string
}

function toAccount(row: AccountRecord): AccountRow {
  return {
    id: row.id,
    provider: row.provider,
    login: row.login,
    name: row.name,
    kind: row.kind,
    avatarUrl: row.avatar_url,
    url: row.url,
    status: row.status,
    lastError: row.last_error,
    connection: JSON.parse(row.connection) as Connection,
    seenAt: row.seen_at,
  }
}

interface RepoRecord {
  id: string
  provider: ProviderKind
  account_id: string
  full_name: string
  snapshot: string | null
  synced_at: string | null
  sync_error: string | null
  seen_at: string
}

function toRepo(row: RepoRecord): RepoRow {
  return {
    id: row.id,
    provider: row.provider,
    accountId: row.account_id,
    fullName: row.full_name,
    snapshot: row.snapshot ? (JSON.parse(row.snapshot) as RepoSnapshot) : null,
    syncedAt: row.synced_at,
    syncError: row.sync_error,
    seenAt: row.seen_at,
  }
}
