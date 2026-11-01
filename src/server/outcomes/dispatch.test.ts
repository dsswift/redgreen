import { afterEach, describe, expect, it, vi } from 'vitest'
import { classify, outcomes, type DeploymentRecord } from '../../shared/outcomes.ts'
import type { Config } from '../config.ts'
import { createLogger } from '../log.ts'
import type { Store } from '../store.ts'
import { createTelemetry, type Telemetry } from '../telemetry.ts'
import { DispatchIncidents, mapDispatchIncidents, type DispatchIncident } from './dispatch.ts'
import type { OrreryWorkload } from './orrery-reader.ts'

// `GET /v1/incidents` as the desk answers it: every field it writes, empty ones left out.
const fixture: DispatchIncident[] = JSON.parse(`[
  {"id":12,"title":"Payments API is down","severity":"critical","state":"acknowledged","service":"payments","workload":"payments","group_key":"PaymentsDown/payments","owner":"user@example.com","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-10-30T12:00:00Z","acknowledged_at":"2026-10-30T12:05:00Z","updated_at":"2026-10-30T12:05:00Z","last_update":"Acknowledged","firing_alerts":1},
  {"id":11,"title":"Board is slow","severity":"warning","state":"resolved","service":"board","workload":"board","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-10-28T09:00:00Z","acknowledged_at":"2026-10-28T09:10:00Z","mitigated_at":"2026-10-28T10:00:00Z","resolved_at":"2026-10-28T11:00:00Z","updated_at":"2026-10-28T11:00:00Z","last_update":"Resolved","firing_alerts":0},
  {"id":10,"title":"Certificate renews soon","severity":"info","state":"resolved","service":"payments","workload":"payments","source":"alertmanager","auto_resolve":true,"public":false,"opened_at":"2026-10-27T09:00:00Z","resolved_at":"2026-10-27T09:30:00Z","updated_at":"2026-10-27T09:30:00Z","last_update":"Resolved","firing_alerts":0},
  {"id":9,"title":"Opened by hand","severity":"critical","state":"resolved","service":"payments","source":"desk","auto_resolve":false,"public":true,"opened_at":"2026-10-26T09:00:00Z","resolved_at":"2026-10-26T12:00:00Z","updated_at":"2026-10-26T12:00:00Z","last_update":"Resolved","firing_alerts":0},
  {"id":8,"title":"Node pressure","severity":"warning","state":"open","service":"kube-system","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-10-29T09:00:00Z","updated_at":"2026-10-29T09:00:00Z","last_update":"Opened","firing_alerts":2},
  {"id":7,"title":"Vault sealed","severity":"critical","state":"resolved","service":"vault","workload":"vault","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-10-25T09:00:00Z","resolved_at":"2026-10-25T10:00:00Z","updated_at":"2026-10-25T10:00:00Z","last_update":"Resolved","firing_alerts":0},
  {"id":6,"title":"Elsewhere","severity":"critical","state":"resolved","service":"other","workload":"other","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-10-25T09:00:00Z","resolved_at":"2026-10-25T10:00:00Z","updated_at":"2026-10-25T10:00:00Z","last_update":"Resolved","firing_alerts":0},
  {"id":1,"title":"Long ago","severity":"critical","state":"resolved","service":"payments","workload":"payments","source":"alertmanager","auto_resolve":false,"public":false,"opened_at":"2026-01-01T00:00:00Z","resolved_at":"2026-01-01T01:00:00Z","updated_at":"2026-01-01T01:00:00Z","last_update":"Resolved","firing_alerts":0}
]`)

const workloads: OrreryWorkload[] = [
  { key: 'payments', fields: { repo: 'https://github.com/example/payments' } },
  { key: 'board', fields: { repo: 'https://github.com/example/platform/tree/main/workloads/board' } },
  { key: 'vault', fields: { repo: null } },
  { key: 'other', fields: { repo: 'https://github.com/example/untracked' } },
]
const tracked: Record<string, string> = { 'github:example/payments': 'github:1', 'github:example/platform': 'github:2' }
const resolve = (ref: { provider: string; fullName: string }) => tracked[`${ref.provider}:${ref.fullName}`] ?? null
const since = '2026-10-01T00:00:00Z'

describe('mapDispatchIncidents', () => {
  it('ties an incident to the repository and folder its workload names, restored when it was mitigated', () => {
    const out = mapDispatchIncidents(fixture, since, workloads, resolve, 'gitlab.com')
    expect(out.incidents).toEqual([
      { repoId: 'github:1', service: '', openedAt: '2026-10-30T12:00:00Z', resolvedAt: null },
      { repoId: 'github:2', service: 'workloads/board', openedAt: '2026-10-28T09:00:00Z', resolvedAt: '2026-10-28T10:00:00Z' },
      { repoId: 'github:1', service: '', openedAt: '2026-10-26T09:00:00Z', resolvedAt: '2026-10-26T12:00:00Z' },
    ])
  })

  it('says why each incident it cannot place was left out', () => {
    expect(mapDispatchIncidents(fixture, since, workloads, resolve, 'gitlab.com').skipped).toEqual({ before: 1, info: 1, no_workload: 1, no_repo_url: 1, repo_not_tracked: 1 })
  })
})

describe('incidents from dispatch in outcomes', () => {
  const deploy = (finishedAt: string): DeploymentRecord => ({ key: `github:1/forge/${finishedAt}`, repoId: 'github:1', service: '', source: 'forge', environment: 'production', status: 'succeeded', revision: 'abc', revisionElsewhere: false, startedAt: null, finishedAt, url: null, baseRevision: null, commits: [] })

  it('fails the deployment an incident followed, and counts it in change failure rate and time to restore', () => {
    const { incidents } = mapDispatchIncidents(fixture, since, workloads, resolve, 'gitlab.com')
    const records = [deploy('2026-10-26T08:00:00Z'), deploy('2026-10-20T08:00:00Z')]
    const classified = classify(records, incidents)
    expect(classified.map((d) => [d.finishedAt, d.failure, d.restoredAt])).toEqual([
      ['2026-10-20T08:00:00Z', null, null],
      ['2026-10-26T08:00:00Z', 'incident', '2026-10-26T12:00:00Z'],
    ])
    const names = () => ({ fullName: 'example/payments', name: 'payments' })
    const { estate } = outcomes({ records, incidents, names, window: 30, now: Date.parse('2026-10-31T00:00:00Z') })
    expect(estate.current.changeFailureRate).toBe(0.5)
    expect(estate.current.timeToRestoreP50Hours).toBe(4)
  })
})

describe('DispatchIncidents', () => {
  const config = { DISPATCH_URL: 'http://dispatch.test:8081/', DISPATCH_TOKEN: 'secret', GITLAB_URL: 'https://gitlab.com' } as Config
  const store = { findRepoByFullName: (provider: string, fullName: string) => (tracked[`${provider}:${fullName}`] ? { id: tracked[`${provider}:${fullName}`] } : null) } as unknown as Store
  const catalog = { workloads: () => workloads, enabled: () => true }
  const counted = (): { telemetry: Telemetry; reads: string[] } => {
    const reads: string[] = []
    const telemetry = { ...createTelemetry({}, '0.0.0', createLogger('test')), incidentsRead: (source: string, outcome: string) => reads.push(`${source}:${outcome}`) }
    return { telemetry, reads }
  }

  afterEach(() => vi.unstubAllGlobals())

  it('reads the list with its token, once per minute, and counts the read', async () => {
    const fetch = vi.fn(async (_url: string, _init: RequestInit) => new Response(JSON.stringify(fixture), { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    const { telemetry, reads } = counted()
    const source = new DispatchIncidents(config, catalog, store, telemetry, createLogger('test'))
    expect(await source.incidents(since)).toHaveLength(3)
    expect(await source.incidents(since)).toHaveLength(3)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch.mock.calls[0]![0]).toBe('http://dispatch.test:8081/v1/incidents?limit=5000')
    expect(fetch.mock.calls[0]![1].headers).toMatchObject({ authorization: 'Bearer secret' })
    expect(reads).toEqual(['dispatch:ok'])
  })

  it('answers nothing when the desk refuses, counts the failure, and tries again next time', async () => {
    const fetch = vi.fn(async () => new Response('this token is not valid', { status: 401 }))
    vi.stubGlobal('fetch', fetch)
    const { telemetry, reads } = counted()
    const source = new DispatchIncidents(config, catalog, store, telemetry, createLogger('test'))
    expect(await source.incidents(since)).toEqual([])
    expect(await source.incidents(since)).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(reads).toEqual(['dispatch:failed', 'dispatch:failed'])
  })

  it('is off without both settings', () => {
    const { telemetry } = counted()
    expect(new DispatchIncidents({ ...config, DISPATCH_TOKEN: undefined }, catalog, store, telemetry, createLogger('test')).enabled()).toBe(false)
  })
})
