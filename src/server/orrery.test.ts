import { describe, expect, it } from 'vitest'
import type { RepoSnapshot, Run } from '../shared/model.ts'
import type { RepoHealth } from '../shared/rules.ts'
import { toService } from './orrery.ts'

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

describe('toService', () => {
  it('shapes a repo into a service entity', () => {
    const health = { level: 'red', quietReason: null, signals: [{ rule: 'default_branch_failing', severity: 'red', title: 'ci is failing on main', detail: '', since: null, sinceIsLowerBound: false, url: null, pipelineId: 'p' }], running: false, successRate: 0.66, lastRunAt: null } as unknown as RepoHealth
    const s = toService(repo, { login: 'Example' }, health, 'https://board/repos/github%3A1', Date.parse('2026-09-30T00:00:00Z'))
    expect(s.key).toBe('github/example/payments')
    expect(s.title).toBe('Payments')
    expect(s.fields).toMatchObject({ language: 'Go', organization: 'Example', pipelines_failing: 1, workflow_failure_rate: 33, stale_prs: 1, last_release: 'v1.2.0', dependabot_critical: 1, code_scanning_critical: null, health: 'red', health_reason: 'ci is failing on main', branch_protected: true, required_approvals: 2, has_ci: true })
  })

  it('reports grey for quiet and unknown, and omits facts it does not have', () => {
    const { facts: _f, ...bare } = repo
    const s = toService({ ...bare, pipelines: [] }, { login: 'x' }, null, 'b')
    expect(s.fields.health).toBe('grey')
    expect(s.fields.has_ci).toBe(false)
    expect('branch_protected' in s.fields).toBe(false)
  })
})
