import { existsSync, mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { describe, expect, it } from 'vitest'
import { MAX_CACHED_BODY, Store } from './store.ts'

const NOW = '2026-10-23T12:00:00.000Z'

describe('http cache', () => {
  it('keeps a body up to the limit', () => {
    const store = new Store(':memory:')
    store.putCached('k', 'e1', 'x'.repeat(MAX_CACHED_BODY), NOW)
    expect(store.getCached('k')?.etag).toBe('e1')
  })

  it('does not keep a body over the limit, and drops what it kept for the key', () => {
    const store = new Store(':memory:')
    store.putCached('k', 'e1', 'small', NOW)
    store.putCached('k', 'e2', 'x'.repeat(MAX_CACHED_BODY + 1), NOW)
    expect(store.getCached('k')).toBeNull()
  })

  it('prunes old entries without reading every row', () => {
    const path = join(mkdtempSync(join(tmpdir(), 'redgreen-store-')), 'redgreen.sqlite')
    const store = new Store(path)
    store.putCached('old', 'e', 'body', '2026-10-01T00:00:00.000Z')
    store.putCached('new', 'e', 'body', NOW)
    expect(store.pruneCache('2026-10-16T00:00:00.000Z')).toBe(1)
    store.close()

    const db = new DatabaseSync(path)
    const plan = db.prepare('EXPLAIN QUERY PLAN DELETE FROM http_cache WHERE touched_at < ?').all('x') as { detail: string }[]
    expect(plan.map((p) => p.detail).join(' ')).toContain('USING INDEX http_cache_touched')
  })
})

describe('cache reset', () => {
  it('empties a cache written before the limit and keeps everything else', () => {
    const path = join(mkdtempSync(join(tmpdir(), 'redgreen-store-')), 'redgreen.sqlite')
    const legacy = new DatabaseSync(path)
    legacy.exec(`
      CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE http_cache (key TEXT PRIMARY KEY, etag TEXT NOT NULL, body TEXT NOT NULL, touched_at TEXT NOT NULL);
    `)
    legacy.prepare('INSERT INTO settings (key, value) VALUES (?, ?)').run('rules', '{"stale":"off"}')
    legacy.prepare('INSERT INTO http_cache VALUES (?, ?, ?, ?)').run('tree', 'e', 'x'.repeat(MAX_CACHED_BODY * 4), NOW)
    legacy.close()

    const store = new Store(path)
    expect(store.cacheReset).toBe(true)
    expect(store.getCached('tree')).toBeNull()
    expect(store.getSetting('rules')).toEqual({ stale: 'off' })
    store.close()
    expect(existsSync(`${path}.reset`)).toBe(false)

    expect(new Store(path).cacheReset).toBe(false)
  })
})

describe('deployments', () => {
  const record = { key: 'r/forge/1', repoId: 'r', service: '', source: 'forge' as const, environment: 'production', status: 'succeeded' as const, revision: 'abc', revisionElsewhere: false, startedAt: null, finishedAt: '2026-10-20T00:00:00Z', url: null, baseRevision: null, commits: null }

  it('keeps a deployment and never un-reads its commits', () => {
    const store = new Store(':memory:')
    store.upsertDeployment(record, NOW)
    expect(store.hasDeployment('r/forge/1')).toBe(true)
    expect(store.deploymentsWithoutCommits('r', 10)).toHaveLength(1)
    store.setDeploymentCommits('r/forge/1', null, [{ sha: 'abc', authoredAt: '2026-10-19T00:00:00Z', title: 'feat: x' }])
    store.upsertDeployment({ ...record, status: 'failed' }, NOW)
    const [kept] = store.listDeployments('r')
    expect(kept).toMatchObject({ status: 'failed', commits: [{ sha: 'abc' }] })
    expect(store.deploymentsWithoutCommits('r', 10)).toEqual([])
  })

  it('lists since a time, prunes by age, and goes with its repo', () => {
    const store = new Store(':memory:')
    store.upsertDeployment(record, NOW)
    store.upsertDeployment({ ...record, key: 'r/forge/0', finishedAt: '2026-01-01T00:00:00Z' }, NOW)
    expect(store.listDeployments(null, '2026-06-01T00:00:00Z').map((d) => d.key)).toEqual(['r/forge/1'])
    expect(store.pruneDeployments('2026-06-01T00:00:00Z')).toBe(1)
    store.upsertDiscoveredRepo({ id: 'r', provider: 'github', accountId: 'a', fullName: 'x/y' }, '2026-10-01T00:00:00Z')
    store.pruneRepos('a', NOW)
    expect(store.listDeployments('r')).toEqual([])
  })
})

describe('repository logos', () => {
  const logo = { service: 'github/example/app', name: 'app', type: 'image/svg+xml', sha256: 'a'.repeat(64), data: new Uint8Array([60, 115, 118, 103]) }

  it('keeps the last upload while the name stays, and forgets it for a new name', () => {
    const store = new Store(':memory:')
    store.setRepoIcon('r', logo)
    expect(store.getRepoIcon('r')).toMatchObject({ ...logo, uploadedSha256: null })
    store.setRepoIconUploaded('r', logo.sha256)
    store.setRepoIcon('r', { ...logo, sha256: 'b'.repeat(64) })
    expect(store.getRepoIcon('r')?.uploadedSha256).toBe(logo.sha256)
    store.setRepoIcon('r', { ...logo, name: 'other' })
    expect(store.getRepoIcon('r')?.uploadedSha256).toBeNull()
    store.setRepoIcon('r', null)
    expect(store.getRepoIcon('r')).toBeNull()
  })
})
