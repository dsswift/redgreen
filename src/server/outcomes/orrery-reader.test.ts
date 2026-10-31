import { describe, expect, it } from 'vitest'
import { mapOrreryDeployments, type OrreryApp, type OrreryDeployment, type OrreryWorkload } from './orrery-reader.ts'

const sync = (key: string, app: string, over: Partial<OrreryDeployment['fields']> = {}): OrreryDeployment => ({
  key,
  fields: { status: 'succeeded', revision: 'abc', started_at: '2026-10-30T10:00:00Z', deployed_at: '2026-10-30T10:00:05Z', ...over },
  relations: { app: [{ key: app }] },
})

const apps: OrreryApp[] = [
  { key: 'home/argocd/redgreen', fields: { repo_url: 'https://github.com/spraguehouse/sh-monorepo' }, relations: { workload: [{ key: 'redgreen' }], environment: [{ key: 'production' }] } },
  { key: 'home/argocd/spraguehouse', fields: { repo_url: 'https://github.com/spraguehouse/sh-monorepo' }, relations: { workload: [{ key: 'spraguehouse' }] } },
  { key: 'home/argocd/cert-manager', fields: { repo_url: 'https://charts.jetstack.io' }, relations: {} },
  { key: 'home/argocd/vaultwarden', fields: { repo_url: 'https://github.com/spraguehouse/sh-monorepo' }, relations: { workload: [{ key: 'vaultwarden' }] } },
  { key: 'home/argocd/petri', fields: { repo_url: 'https://github.com/spraguehouse/sh-monorepo' }, relations: { workload: [{ key: 'petri' }] } },
]
const workloads: OrreryWorkload[] = [
  { key: 'redgreen', fields: { repo: 'https://github.com/dsswift/redgreen' } },
  { key: 'spraguehouse', fields: { repo: 'https://github.com/spraguehouse/sh-monorepo/tree/main/workloads/spraguehouse' } },
  { key: 'vaultwarden', fields: { repo: null } },
  { key: 'petri', fields: { repo: 'https://github.com/dsswift/petri' } },
]
const tracked: Record<string, string> = { 'github:dsswift/redgreen': 'github:1', 'github:spraguehouse/sh-monorepo': 'github:2' }
const resolve = (ref: { provider: string; fullName: string }) => tracked[`${ref.provider}:${ref.fullName}`] ?? null

describe('mapOrreryDeployments', () => {
  it('keeps a sync against the workload repository, with the folder when it is one service of a monorepo', () => {
    const out = mapOrreryDeployments([sync('home/argocd/redgreen/3', 'home/argocd/redgreen'), sync('home/argocd/spraguehouse/9', 'home/argocd/spraguehouse', { status: 'failed' })], apps, workloads, resolve, 'gitlab.com')
    expect(out.records).toEqual([
      expect.objectContaining({ key: 'github:1/orrery/home/argocd/redgreen/3', repoId: 'github:1', service: '', source: 'orrery', environment: 'production', status: 'succeeded', revision: 'abc', revisionElsewhere: true, finishedAt: '2026-10-30T10:00:05Z', commits: null }),
      expect.objectContaining({ key: 'github:2/orrery/home/argocd/spraguehouse/9', repoId: 'github:2', service: 'workloads/spraguehouse', environment: 'production', status: 'failed', revisionElsewhere: false }),
    ])
  })

  it('says why each sync it cannot place was left out', () => {
    const out = mapOrreryDeployments(
      [
        sync('a', 'home/argocd/cert-manager'),
        sync('b', 'home/argocd/vaultwarden'),
        sync('c', 'home/argocd/petri'),
        sync('d', 'home/argocd/redgreen', { deployed_at: null }),
        { key: 'e', fields: { status: 'succeeded', revision: 'x', deployed_at: '2026-10-30T10:00:05Z' }, relations: {} },
      ],
      apps,
      workloads,
      resolve,
      'gitlab.com',
    )
    expect(out.records).toEqual([])
    expect(out.skipped).toEqual({ no_app: 1, no_workload: 1, no_repo_url: 1, repo_not_tracked: 1, unfinished: 1 })
  })
})
