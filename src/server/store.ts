import { existsSync, mkdirSync, renameSync, rmSync } from 'node:fs'
import { dirname } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import type { Account, ProviderKind, RepoSnapshot } from '../shared/model.ts'
import type { CarriedCommit, DeploymentRecord } from '../shared/outcomes.ts'
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
CREATE TABLE IF NOT EXISTS deployments (
  key TEXT PRIMARY KEY,
  repo_id TEXT NOT NULL,
  service TEXT NOT NULL,
  source TEXT NOT NULL,
  environment TEXT NOT NULL,
  status TEXT NOT NULL,
  revision TEXT NOT NULL,
  revision_elsewhere INTEGER NOT NULL,
  started_at TEXT,
  finished_at TEXT NOT NULL,
  url TEXT,
  base_revision TEXT,
  commits TEXT,
  seen_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS deployments_repo ON deployments(repo_id, finished_at);
CREATE INDEX IF NOT EXISTS deployments_finished ON deployments(finished_at);
`

/**
 * The largest response body the HTTP cache keeps. Every query on this
 * database runs on the event loop, so a cache that grows without bound stalls
 * the server, health checks included. A body this large is almost always
 * addressed by a commit and never asked for again.
 */
export const MAX_CACHED_BODY = 256 * 1024

/**
 * Raised when the HTTP cache must start again empty. A file's `PRAGMA
 * user_version` records the generation it was written with. Generation 1
 * bounded the cache: before it, whole file trees piled up by the hundred.
 */
const CACHE_GENERATION = 1

/** Every table but the cache. A cache reset copies these and nothing else. */
const KEPT_TABLES = ['settings', 'accounts', 'repos', 'repo_settings', 'level_history', 'deployments']

/**
 * Starts the cache again empty when the file predates `CACHE_GENERATION`.
 * Dropping a table reads every page it frees, which takes minutes on a slow
 * volume, so the kept tables are copied into a new file that replaces the
 * old one. The old file is untouched until the copy is complete. Answers
 * whether the cache was reset.
 */
function resetCache(path: string): boolean {
  if (!existsSync(path)) return false
  const fresh = `${path}.reset`
  const old = new DatabaseSync(path)
  try {
    const { user_version: generation } = old.prepare('PRAGMA user_version').get() as { user_version: number }
    if (generation >= CACHE_GENERATION) return false
    rmSync(fresh, { force: true })
    const next = new DatabaseSync(fresh)
    next.exec(SCHEMA)
    next.exec(`PRAGMA user_version = ${CACHE_GENERATION}`)
    next.close()
    old.prepare('ATTACH DATABASE ? AS fresh').run(fresh)
    old.exec('BEGIN')
    for (const table of KEPT_TABLES) {
      const columns = (old.prepare(`PRAGMA main.table_info(${table})`).all() as { name: string }[]).map((c) => `"${c.name}"`).join(', ')
      if (columns) old.exec(`INSERT INTO fresh.${table} (${columns}) SELECT ${columns} FROM main.${table}`)
    }
    old.exec('COMMIT')
    old.exec('DETACH DATABASE fresh')
  } finally {
    old.close()
  }
  renameSync(fresh, path)
  for (const suffix of ['-wal', '-shm']) rmSync(path + suffix, { force: true })
  return true
}

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
  /** True when this open started the HTTP cache again empty. */
  readonly cacheReset: boolean

  constructor(path: string) {
    const file = path !== ':memory:'
    if (file) mkdirSync(dirname(path), { recursive: true })
    this.cacheReset = file && resetCache(path)
    this.db = new DatabaseSync(path)
    this.db.exec('PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA foreign_keys = ON;')
    this.db.exec(SCHEMA)
    this.db.exec(`PRAGMA user_version = ${CACHE_GENERATION}`)
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
      this.db.prepare('DELETE FROM deployments WHERE repo_id = ?').run(id)
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

  // Deployments ----------------------------------------------------------------

  /** Keeps a deployment. One already kept takes the new facts but keeps the commits it has read; a source never un-reads them. */
  upsertDeployment(d: DeploymentRecord, now: string): void {
    this.db
      .prepare(
        `INSERT INTO deployments (key, repo_id, service, source, environment, status, revision, revision_elsewhere, started_at, finished_at, url, base_revision, commits, seen_at)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(key) DO UPDATE SET
           repo_id = excluded.repo_id, service = excluded.service, environment = excluded.environment, status = excluded.status,
           revision = excluded.revision, revision_elsewhere = excluded.revision_elsewhere, started_at = excluded.started_at,
           finished_at = excluded.finished_at, url = excluded.url, seen_at = excluded.seen_at,
           base_revision = CASE WHEN deployments.commits IS NULL THEN excluded.base_revision ELSE deployments.base_revision END,
           commits = COALESCE(deployments.commits, excluded.commits)`,
      )
      .run(d.key, d.repoId, d.service, d.source, d.environment, d.status, d.revision, d.revisionElsewhere ? 1 : 0, d.startedAt, d.finishedAt, d.url, d.baseRevision, d.commits ? JSON.stringify(d.commits) : null, now)
  }

  /** Whether a deployment is already kept. */
  hasDeployment(key: string): boolean {
    return !!this.db.prepare('SELECT 1 FROM deployments WHERE key = ?').get(key)
  }

  /** Every deployment, or one repo's, that finished at or after `since`, oldest first. */
  listDeployments(repoId: string | null = null, since: string | null = null): DeploymentRecord[] {
    const rows = this.db
      .prepare(`SELECT * FROM deployments WHERE (? IS NULL OR repo_id = ?) AND (? IS NULL OR finished_at >= ?) ORDER BY finished_at`)
      .all(repoId, repoId, since, since) as unknown as DeploymentRecordRow[]
    return rows.map(toDeployment)
  }

  /** One repo's deployments whose commits are not read yet, newest first. */
  deploymentsWithoutCommits(repoId: string, limit: number): DeploymentRecord[] {
    const rows = this.db.prepare('SELECT * FROM deployments WHERE repo_id = ? AND commits IS NULL ORDER BY finished_at DESC LIMIT ?').all(repoId, limit) as unknown as DeploymentRecordRow[]
    return rows.map(toDeployment)
  }

  setDeploymentCommits(key: string, baseRevision: string | null, commits: CarriedCommit[]): void {
    this.db.prepare('UPDATE deployments SET base_revision = ?, commits = ? WHERE key = ?').run(baseRevision, JSON.stringify(commits), key)
  }

  pruneDeployments(finishedBefore: string): number {
    return Number(this.db.prepare('DELETE FROM deployments WHERE finished_at < ?').run(finishedBefore).changes)
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

interface DeploymentRecordRow {
  key: string
  repo_id: string
  service: string
  source: DeploymentRecord['source']
  environment: string
  status: DeploymentRecord['status']
  revision: string
  revision_elsewhere: number
  started_at: string | null
  finished_at: string
  url: string | null
  base_revision: string | null
  commits: string | null
  seen_at: string
}

function toDeployment(row: DeploymentRecordRow): DeploymentRecord {
  return {
    key: row.key,
    repoId: row.repo_id,
    service: row.service,
    source: row.source,
    environment: row.environment,
    status: row.status,
    revision: row.revision,
    revisionElsewhere: row.revision_elsewhere === 1,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    url: row.url,
    baseRevision: row.base_revision,
    commits: row.commits ? (JSON.parse(row.commits) as CarriedCommit[]) : null,
  }
}
