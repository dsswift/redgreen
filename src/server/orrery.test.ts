import { describe, expect, it } from 'vitest'
import type { RepoSnapshot, Run } from '../shared/model.ts'
import type { RepoHealth } from '../shared/rules.ts'
import { DEPLOYMENT_BLUEPRINT, REPOSITORY_BLUEPRINT, toDeployments, toRepository } from './orrery.ts'

const run = (conclusion: Run['conclusion'], ref = 'main'): Run => ({ id: 'r', number: 1, title: 't', trigger: 'push', ref, sha: 's', status: 'completed', conclusion, createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z', url: 'u', actor: null })

const repo: RepoSnapshot = {
  id: 'github:1',
  provider: 'github',
  accountId: 'github:9',
  name: 'Payments',
  fullName: 'Example/Payments',
  url: 'https://github.com/example/payments',
  description: 'Takes money',
  defaultBranch: 'main',
  visibility: 'private',
  archived: false,
  fork: false,
  language: 'Go',
  pushedAt: '2026-09-20T00:00:00Z',
  stars: 3,
  openIssues: 2,
  openPullRequests: 2,
  pipelines: [{ id: 'p', name: 'ci', path: '.github/workflows/ci.yml', url: 'u', state: 'enabled', createdAt: '', updatedAt: '', schedules: [], runs: [run('failure'), run('success'), run('success'), run('success', 'feature')] }],
  releases: [{ tag: 'v1.2.0', name: 'v1.2.0', publishedAt: '2026-09-10T00:00:00Z', url: 'u', prerelease: false }],
  pullRequests: [
    { number: 1, title: 'old', url: 'u', author: 'a', draft: false, updatedAt: '2026-09-01T00:00:00Z', checks: 'success' },
    { number: 2, title: 'new', url: 'u', author: 'a', draft: false, updatedAt: '2026-09-29T00:00:00Z', checks: 'success' },
  ],
  security: { dependabot: { critical: 1, high: 2, url: 'u' }, codeScanning: null },
  facts: { topics: ['billing'], branchProtected: true, requiresReview: true, requiredApprovals: 2, requiresCodeOwnerReview: false, hasCodeowners: true, hasReadme: true, hasDockerfile: false, lastCommitter: 'dev', lastCommitAt: '2026-09-20T00:00:00Z' },
  syncedAt: '2026-09-30T00:00:00Z',
}

describe('toRepository', () => {
  it('shapes a repo into a repository entity', () => {
    const health = { level: 'red', quietReason: null, signals: [{ rule: 'default_branch_failing', severity: 'red', title: 'ci is failing on main', detail: '', since: null, sinceIsLowerBound: false, url: null, pipelineId: 'p' }], running: false, successRate: 0.66, lastRunAt: null } as unknown as RepoHealth
    const s = toRepository(repo, { login: 'Example' }, health, 'https://board/repos/github%3A1', Date.parse('2026-09-30T00:00:00Z'))
    expect(s.blueprint).toBe('repository')
    expect(s.key).toBe('github/example/payments')
    expect(s.title).toBe('Payments')
    expect(s.fields).toMatchObject({ language: 'Go', organization: 'Example', pipelines_failing: 1, workflow_failure_rate: 33, stale_prs: 1, last_release: 'v1.2.0', dependabot_critical: 1, code_scanning_critical: null, health: 'red', health_reason: 'ci is failing on main', branch_protected: true, required_approvals: 2, has_ci: true })
  })

  it('reports grey for quiet and unknown, and omits facts it does not have', () => {
    const { facts: _f, ...bare } = repo
    const s = toRepository({ ...bare, pipelines: [] }, { login: 'x' }, null, 'b')
    expect(s.fields.health).toBe('grey')
    expect(s.fields.has_ci).toBe(false)
    expect('branch_protected' in s.fields).toBe(false)
  })

  it('writes only fields the blueprint it offers defines', () => {
    const s = toRepository(repo, { login: 'Example' }, null, 'b')
    expect(Object.keys(s.fields).filter((f) => !(f in REPOSITORY_BLUEPRINT.fields))).toEqual([])
  })
})

describe('toDeployments', () => {
  const deployment = (id: string, status: 'succeeded' | 'failed' | 'running' | 'other', finishedAt: string | null) => ({ id, environment: 'production', status, ref: 'main', sha: 'abc', createdAt: '2026-09-20T10:00:00Z', finishedAt, url: null, actor: 'dev' })

  it('shapes each finished deployment and points it at its repository', () => {
    const out = toDeployments({ ...repo, deployments: [deployment('7', 'succeeded', '2026-09-20T10:01:30Z'), deployment('8', 'failed', '2026-09-21T10:00:10Z'), deployment('9', 'running', null), deployment('10', 'other', null)] })
    expect(out.map((d) => d.key)).toEqual(['github/example/payments/7', 'github/example/payments/8'])
    expect(out[0]).toMatchObject({ blueprint: 'deployment', title: 'Payments to production', relations: { repository: 'github/example/payments' }, fields: { status: 'succeeded', environment_name: 'production', revision: 'abc', deployed_at: '2026-09-20T10:01:30Z', duration_seconds: 90, initiated_by: 'dev' } })
    expect(Object.keys(out[0]!.fields).filter((f) => !(f in DEPLOYMENT_BLUEPRINT.fields))).toEqual([])
  })

  it('has nothing to say for a forge that keeps no deployments', () => {
    expect(toDeployments({ ...repo, deployments: null })).toEqual([])
    expect(toDeployments(repo)).toEqual([])
  })
})
