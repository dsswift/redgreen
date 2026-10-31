import { describe, expect, it } from 'vitest'
import type { RepoSnapshot } from '../../shared/model.ts'
import type { CarriedCommit } from '../../shared/outcomes.ts'
import { createLogger } from '../log.ts'
import type { Provider } from '../providers/provider.ts'
import { Store, type AccountRow, type RepoRow } from '../store.ts'
import { createTelemetry } from '../telemetry.ts'
import { Outcomes, forgeRecords, standIn } from './outcomes.ts'

const log = createLogger('test')
const telemetry = createTelemetry({}, '0.0.0', log)

const snapshot: RepoSnapshot = {
  id: 'github:1',
  provider: 'github',
  accountId: 'github:9',
  name: 'payments',
  fullName: 'example/payments',
  url: 'u',
  description: null,
  defaultBranch: 'main',
  visibility: 'private',
  archived: false,
  fork: false,
  language: null,
  pushedAt: null,
  stars: 0,
  openIssues: 0,
  openPullRequests: 0,
  pipelines: [],
  releases: [
    { tag: 'v1.1.0', name: 'v1.1.0', publishedAt: '2026-10-20T00:00:00Z', url: 'u', prerelease: false },
    { tag: 'v1.0.0', name: 'v1.0.0', publishedAt: '2026-10-10T00:00:00Z', url: 'u', prerelease: false },
    { tag: 'v1.2.0-rc1', name: 'rc', publishedAt: '2026-10-25T00:00:00Z', url: 'u', prerelease: true },
  ],
  pullRequests: [],
  security: { dependabot: null, codeScanning: null },
  deployments: [
    { id: '1', environment: 'production', status: 'succeeded', ref: 'main', sha: 'aaa', createdAt: '2026-10-01T10:00:00Z', finishedAt: '2026-10-01T10:05:00Z', url: 'd1', actor: null },
    { id: '2', environment: 'production', status: 'failed', ref: 'main', sha: 'bbb', createdAt: '2026-10-02T10:00:00Z', finishedAt: '2026-10-02T10:05:00Z', url: null, actor: null },
    { id: '3', environment: 'production', status: 'running', ref: 'main', sha: 'ccc', createdAt: '2026-10-03T10:00:00Z', finishedAt: null, url: null, actor: null },
  ],
  services: { commit: 'x', rules: 1, services: [{ id: '', path: '', name: 'payments', type: 'backend', language: null }] },
  syncedAt: '2026-10-31T00:00:00Z',
}
const account: AccountRow = { id: 'github:9', provider: 'github', login: 'example', name: 'Example', kind: 'organization', avatarUrl: null, url: 'u', status: 'active', lastError: null, connection: { type: 'github-token' }, seenAt: 'now' }

function fakeProvider(asked: [string | null, string][], answer: (base: string | null, head: string) => CarriedCommit[] | null): Provider {
  return {
    kind: 'github',
    configured: () => true,
    status: () => ({ kind: 'github', configured: true, source: null, links: [], rateLimits: [] }),
    discoverAccounts: async () => [],
    discoverRepos: async () => [],
    syncRepo: async () => snapshot,
    files: async () => null,
    commitsBetween: async (_a, _r, base, head) => {
      asked.push([base, head])
      return answer(base, head)
    },
  }
}

function setup(provider: Provider) {
  const store = new Store(':memory:')
  store.upsertAccount({ ...account }, 'now')
  store.upsertDiscoveredRepo({ id: 'github:1', provider: 'github', accountId: 'github:9', fullName: 'example/payments' }, 'now')
  store.saveSnapshot(snapshot)
  const row = store.getRepo('github:1') as RepoRow
  return { store, row, outcomes: new Outcomes(store, [provider], telemetry, log) }
}

describe('forgeRecords', () => {
  it('keeps the finished deployments of the whole repository', () => {
    expect(forgeRecords('github:1', snapshot).map((r) => [r.key, r.status, r.finishedAt, r.url])).toEqual([
      ['github:1/forge/1', 'succeeded', '2026-10-01T10:05:00Z', 'd1'],
      ['github:1/forge/2', 'failed', '2026-10-02T10:05:00Z', null],
    ])
  })
})

describe('standIn', () => {
  it('picks the newest full release at or before the time', () => {
    expect(standIn(snapshot.releases, '2026-10-15T00:00:00Z')).toBe('v1.0.0')
    expect(standIn(snapshot.releases, '2026-10-26T00:00:00Z')).toBe('v1.1.0')
    expect(standIn(snapshot.releases, '2026-10-01T00:00:00Z')).toBeNull()
  })
})

describe('Outcomes', () => {
  it('keeps forge deployments once and reads the commits between each and the previous success', async () => {
    const asked: [string | null, string][] = []
    const { store, row, outcomes } = setup(fakeProvider(asked, (base, head) => [{ sha: head, authoredAt: '2026-10-01T00:00:00Z', title: base ? 'feat: more' : 'feat: first' }]))
    await outcomes.ingestRepo(row, snapshot, account)
    expect(asked).toEqual([
      ['aaa', 'bbb'],
      [null, 'aaa'],
    ])
    const kept = store.listDeployments('github:1')
    expect(kept.map((d) => [d.key, d.baseRevision, d.commits?.length])).toEqual([
      ['github:1/forge/1', null, 1],
      ['github:1/forge/2', 'aaa', 1],
    ])
    await outcomes.ingestRepo(row, snapshot, account)
    expect(asked).toHaveLength(2)
  })

  it('uses release tags for a revision that lives elsewhere, and counts nothing between the same tag', async () => {
    const asked: [string | null, string][] = []
    const { store, outcomes } = setup(fakeProvider(asked, () => []))
    const base = { repoId: 'github:1', service: '', source: 'orrery' as const, environment: 'production', status: 'succeeded' as const, revision: 'manifest-sha', revisionElsewhere: true, startedAt: null, url: null, baseRevision: null, commits: null }
    outcomes.keep([
      { ...base, key: 'github:1/orrery/a', finishedAt: '2026-10-05T00:00:00Z' },
      { ...base, key: 'github:1/orrery/b', finishedAt: '2026-10-12T00:00:00Z' },
      { ...base, key: 'github:1/orrery/c', finishedAt: '2026-10-14T00:00:00Z' },
      { ...base, key: 'github:1/orrery/d', finishedAt: '2026-10-21T00:00:00Z' },
    ])
    await outcomes.readCommits('github:1')
    expect(asked).toEqual([
      ['v1.0.0', 'v1.1.0'],
      [null, 'v1.0.0'],
    ])
    expect(store.listDeployments('github:1').map((d) => [d.key, d.baseRevision, d.commits])).toEqual([
      ['github:1/orrery/a', null, []],
      ['github:1/orrery/b', null, []],
      ['github:1/orrery/c', 'v1.0.0', []],
      ['github:1/orrery/d', 'v1.0.0', []],
    ])
  })

  it('computes the page and the catalog measures from what it keeps', async () => {
    const { row, outcomes } = setup(fakeProvider([], (_b, head) => [{ sha: head, authoredAt: '2026-10-01T00:00:00Z', title: 'feat: x' }]))
    const now = Date.parse('2026-10-20T00:00:00Z')
    await outcomes.ingestRepo(row, snapshot, account)
    const page = await outcomes.compute(30, now)
    expect(page.rows.map((r) => [r.fullName, r.name, r.current.deployments, r.current.changeFailureRate])).toEqual([['example/payments', 'payments', 2, 0.5]])
    expect(page.estate.current.frequencyPerDay).toBeCloseTo(1 / 30)
    const m = outcomes.measuresFor('github:1', now)
    expect(m.repo.deployments).toBe(2)
    expect(m.services.get('')?.timeToRestoreP50Hours).toBeNull()
  })
})
