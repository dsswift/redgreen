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
