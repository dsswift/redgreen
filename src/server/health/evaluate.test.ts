import { describe, expect, it } from 'vitest'
import type { Pipeline, RepoSnapshot, Run } from '../../shared/model.ts'
import { DEFAULT_REPO_SETTINGS, defaultRuleSeverities, type RepoSettings } from '../../shared/rules.ts'
import { evaluate } from './evaluate.ts'

const NOW = new Date('2026-09-29T12:00:00Z')

function run(overrides: Partial<Run> & Pick<Run, 'createdAt'>): Run {
  return {
    id: overrides.createdAt,
    number: 1,
    title: 'build',
    trigger: 'push',
    ref: 'main',
    sha: 'abc',
    status: 'completed',
    conclusion: 'success',
    updatedAt: overrides.createdAt,
    url: 'https://example.org/run',
    actor: null,
    ...overrides,
  }
}

function pipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    id: 'p1',
    name: 'CI',
    path: '.github/workflows/ci.yml',
    url: 'https://example.org/ci',
    state: 'enabled',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    schedules: [],
    runs: [],
    ...overrides,
  }
}

function repo(overrides: Partial<RepoSnapshot> = {}): RepoSnapshot {
  return {
    id: 'github:1',
    provider: 'github',
    accountId: 'github:10',
    name: 'repo',
    fullName: 'acme/repo',
    url: 'https://example.org/acme/repo',
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
    releases: [],
    pullRequests: [],
    security: { dependabot: null, codeScanning: null },
    syncedAt: NOW.toISOString(),
    ...overrides,
  }
}

function check(snapshot: RepoSnapshot, settings: Partial<RepoSettings> = {}) {
  return evaluate({ repo: snapshot, rules: defaultRuleSeverities(), settings: { ...DEFAULT_REPO_SETTINGS, ...settings }, now: NOW })
}

describe('evaluate', () => {
  it('is green when the latest default-branch run passed', () => {
    const health = check(repo({ pipelines: [pipeline({ runs: [run({ createdAt: '2026-09-29T10:00:00Z' })] })] }))
    expect(health.level).toBe('green')
    expect(health.successRate).toBe(1)
  })

  it('puts repos without pipelines in the quiet view', () => {
    expect(check(repo())).toMatchObject({ level: 'quiet', quietReason: 'no_pipelines' })
  })

  it('reports how long the default branch has been failing, reading through cancelled runs', () => {
    const health = check(
      repo({
        pipelines: [
          pipeline({
            runs: [
              run({ createdAt: '2026-09-28T00:00:00Z', conclusion: 'failure', number: 9 }),
              run({ createdAt: '2026-09-20T00:00:00Z', conclusion: 'cancelled' }),
              run({ createdAt: '2026-08-19T00:00:00Z', conclusion: 'failure' }),
              run({ createdAt: '2026-08-01T00:00:00Z', conclusion: 'success' }),
              run({ createdAt: '2026-09-27T00:00:00Z', conclusion: 'success', ref: 'feature' }),
            ],
          }),
        ],
      }),
    )
    expect(health.level).toBe('red')
    expect(health.signals[0]).toMatchObject({
      rule: 'default_branch_failing',
      since: '2026-08-19T00:00:00Z',
      sinceIsLowerBound: false,
      detail: 'Last 2 runs failed',
    })
  })

  it('marks the start as a lower bound when history runs out mid-streak', () => {
    const health = check(repo({ pipelines: [pipeline({ runs: [run({ createdAt: '2026-09-28T00:00:00Z', conclusion: 'failure' })] })] }))
    expect(health.signals[0]?.sinceIsLowerBound).toBe(true)
  })

  it('does not judge the default branch on manual runs', () => {
    const health = check(
      repo({
        pipelines: [
          pipeline({ id: 'build', runs: [run({ createdAt: '2026-09-29T00:09:14Z', trigger: 'manual', conclusion: 'failure' })] }),
          pipeline({ id: 'ci', runs: [run({ createdAt: '2026-09-29T02:23:07Z' })] }),
        ],
      }),
    )
    expect(health.level).toBe('green')
    expect(health.signals).toEqual([])
    expect(health.successRate).toBe(1)
  })

  it('ignores failures on muted and disabled pipelines', () => {
    const failing = [run({ createdAt: '2026-09-28T00:00:00Z', conclusion: 'failure' })]
    const health = check(
      repo({ pipelines: [pipeline({ id: 'a', runs: failing }), pipeline({ id: 'b', state: 'disabled', runs: failing })] }),
      { mutedPipelines: ['a'] },
    )
    expect(health).toMatchObject({ level: 'quiet', quietReason: 'no_pipelines' })
  })

  it('applies per-repo severity overrides', () => {
    const snapshot = repo({
      pipelines: [pipeline({ runs: [run({ createdAt: '2026-09-28T00:00:00Z' })] })],
      security: { dependabot: { critical: 2, high: 1, url: 'https://example.org/alerts' }, codeScanning: null },
    })
    expect(check(snapshot).level).toBe('red')
    expect(check(snapshot, { ruleOverrides: { dependabot_critical: 'amber' } }).level).toBe('amber')
    expect(check(snapshot, { ruleOverrides: { dependabot_critical: 'off' } }).level).toBe('green')
  })

  it('surfaces a critical alert on a repo with no pipelines', () => {
    const snapshot = repo({ security: { dependabot: null, codeScanning: { critical: 1, high: 0, url: 'https://example.org/cs' } } })
    expect(check(snapshot)).toMatchObject({ level: 'red', quietReason: null })
  })

  it('keeps archived and muted repos quiet even when failing', () => {
    const failing = repo({ pipelines: [pipeline({ runs: [run({ createdAt: '2026-09-28T00:00:00Z', conclusion: 'failure' })] })] })
    expect(check({ ...failing, archived: true })).toMatchObject({ level: 'quiet', quietReason: 'archived' })
    expect(check(failing, { muted: true })).toMatchObject({ level: 'quiet', quietReason: 'muted' })
  })

  describe('schedules', () => {
    const daily = (runs: Run[]) => repo({ pipelines: [pipeline({ schedules: [{ cron: '0 6 * * *', timezone: 'UTC' }], runs })] })

    it('stays green while a daily schedule keeps running', () => {
      const health = check(daily([run({ createdAt: '2026-09-29T06:04:00Z', trigger: 'schedule' })]))
      expect(health.level).toBe('green')
    })

    it('tolerates a single late run', () => {
      expect(check(daily([run({ createdAt: '2026-09-28T06:04:00Z', trigger: 'schedule' })])).level).toBe('green')
    })

    it('goes amber after two missed fires', () => {
      const health = check(daily([run({ createdAt: '2026-09-27T06:04:00Z', trigger: 'schedule' })]))
      expect(health.level).toBe('amber')
      expect(health.signals[0]).toMatchObject({ rule: 'schedule_stale', since: '2026-09-28T06:00:00.000Z' })
    })

    it('evaluates the cron in the schedule time zone', () => {
      // 06:00 in Tokyo is 21:00 UTC the day before. Read as UTC, the fires at 06:00 on the 28th and 29th would both look missed.
      const tokyo = repo({
        pipelines: [pipeline({ schedules: [{ cron: '0 6 * * *', timezone: 'Asia/Tokyo' }], runs: [run({ createdAt: '2026-09-27T21:04:00Z', trigger: 'schedule' })] })],
      })
      expect(check(tokyo).level).toBe('green')
    })

    it('flags a schedule the forge switched off', () => {
      const health = check(repo({ pipelines: [pipeline({ state: 'dormant', schedules: [{ cron: '0 6 * * *', timezone: 'UTC' }] })] }))
      expect(health.signals[0]).toMatchObject({ rule: 'schedule_stale', title: 'CI schedule switched off' })
    })
  })

  describe('releases', () => {
    it('flags a failed build of the newest tag only', () => {
      const health = check(
        repo({
          pipelines: [
            pipeline({
              runs: [
                run({ createdAt: '2026-09-28T00:00:00Z', trigger: 'tag', ref: 'v2.0.0', conclusion: 'failure' }),
                run({ createdAt: '2026-09-01T00:00:00Z', trigger: 'tag', ref: 'v1.0.0' }),
                run({ createdAt: '2026-09-27T00:00:00Z' }),
              ],
            }),
          ],
        }),
      )
      expect(health.signals).toHaveLength(1)
      expect(health.signals[0]).toMatchObject({ rule: 'release_build_failed', title: 'Release v2.0.0 build failed' })
    })

    it('clears when a retry of the same tag passed', () => {
      const health = check(
        repo({
          pipelines: [
            pipeline({
              runs: [
                run({ createdAt: '2026-09-28T01:00:00Z', trigger: 'tag', ref: 'v2.0.0' }),
                run({ createdAt: '2026-09-28T00:00:00Z', trigger: 'tag', ref: 'v2.0.0', conclusion: 'failure' }),
              ],
            }),
          ],
        }),
      )
      expect(health.level).toBe('green')
    })
  })

  it('counts only ready pull requests with failing checks', () => {
    const pr = { title: 't', url: 'https://example.org/pr', author: null, updatedAt: NOW.toISOString() }
    const health = check(
      repo({
        pipelines: [pipeline({ runs: [run({ createdAt: '2026-09-28T00:00:00Z' })] })],
        pullRequests: [
          { ...pr, number: 1, draft: false, checks: 'failure' },
          { ...pr, number: 2, draft: true, checks: 'failure' },
          { ...pr, number: 3, draft: false, checks: 'success' },
        ],
      }),
    )
    expect(health.signals).toEqual([expect.objectContaining({ rule: 'pr_checks_failing', title: '1 pull request failing checks' })])
  })
})
