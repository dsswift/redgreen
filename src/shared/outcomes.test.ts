import { describe, expect, it } from 'vitest'
import { classify, grade, isRepair, measure, outcomes, percentile, series, type DeploymentRecord } from './outcomes.ts'

const NOW = Date.parse('2026-10-31T12:00:00Z')
const HOUR = 3_600_000
const DAY = 24 * HOUR

let n = 0
const deploy = (daysAgo: number, over: Partial<DeploymentRecord> = {}): DeploymentRecord => {
  const finishedAt = new Date(NOW - daysAgo * DAY).toISOString()
  return {
    key: `k${++n}`,
    repoId: 'github:1',
    service: '',
    source: 'forge',
    environment: 'production',
    status: 'succeeded',
    revision: `r${n}`,
    revisionElsewhere: false,
    startedAt: null,
    finishedAt,
    url: null,
    baseRevision: null,
    commits: [{ sha: `c${n}`, authoredAt: new Date(NOW - daysAgo * DAY - 6 * HOUR).toISOString(), title: 'feat: a thing' }],
    ...over,
  }
}

describe('isRepair', () => {
  it('knows a fix, a revert, and a merge that says nothing', () => {
    expect(isRepair('fix: stop the leak')).toBe(true)
    expect(isRepair('fix(api)!: stop the leak')).toBe(true)
    expect(isRepair('Revert "feat: a thing"')).toBe(true)
    expect(isRepair('revert: feat: a thing')).toBe(true)
    expect(isRepair('feat: a thing')).toBe(false)
    expect(isRepair('Merge pull request #3 from fix/leak')).toBeNull()
  })
})

describe('classify', () => {
  it('counts a failed status as a failure restored by the next success', () => {
    const [bad, good] = classify([deploy(2, { status: 'failed' }), deploy(1)])
    expect(bad).toMatchObject({ failed: true, failure: 'status', restoredAt: good!.finishedAt })
    expect(good).toMatchObject({ failed: false })
  })

  it('counts a success followed within a day by only repairs as a failure, restored by the repair', () => {
    const repair = deploy(1.5, { commits: [{ sha: 'x', authoredAt: '2026-10-30T00:00:00Z', title: 'fix: undo it' }, { sha: 'y', authoredAt: '2026-10-30T00:00:00Z', title: 'Merge branch main' }] })
    const [first, second] = classify([deploy(2), repair])
    expect(first).toMatchObject({ failed: true, failure: 'repair', restoredAt: repair.finishedAt })
    expect(second).toMatchObject({ failed: false })
  })

  it('does not blame a success for a repair more than a day later, or one with features too', () => {
    const late = deploy(0.5, { commits: [{ sha: 'x', authoredAt: '2026-10-30T00:00:00Z', title: 'fix: undo it' }] })
    const mixed = deploy(1.8, { commits: [{ sha: 'x', authoredAt: '2026-10-30T00:00:00Z', title: 'fix: undo it' }, { sha: 'y', authoredAt: '2026-10-30T00:00:00Z', title: 'feat: more' }] })
    const out = classify([deploy(2), mixed, late])
    expect(out.map((d) => d.failed)).toEqual([false, false, false])
  })

  it('judges a stream on its own: another environment or service is not a repair of this one', () => {
    const out = classify([deploy(2), deploy(1.5, { environment: 'staging', commits: [{ sha: 'x', authoredAt: '2026-10-30T00:00:00Z', title: 'fix: it' }] }), deploy(1.4, { service: 'api', commits: [{ sha: 'x', authoredAt: '2026-10-30T00:00:00Z', title: 'fix: it' }] })])
    expect(out.map((d) => d.failed)).toEqual([false, false, false])
  })

  it('counts an incident opened within a day of a success as its failure, restored when the incident resolves', () => {
    const d = deploy(3)
    const openedAt = new Date(Date.parse(d.finishedAt) + 2 * HOUR).toISOString()
    const resolvedAt = new Date(Date.parse(d.finishedAt) + 5 * HOUR).toISOString()
    const [out] = classify([d], [{ repoId: 'github:1', service: '', openedAt, resolvedAt }])
    expect(out).toMatchObject({ failed: true, failure: 'incident', restoredAt: resolvedAt })
  })
})

describe('percentile', () => {
  it('uses nearest rank', () => {
    expect(percentile([], 50)).toBeNull()
    expect(percentile([5], 90)).toBe(5)
    expect(percentile([1, 2, 3, 4], 50)).toBe(2)
    expect(percentile([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 90)).toBe(9)
  })
})

describe('measure', () => {
  it('computes the four numbers over a window', () => {
    const records = [deploy(1), deploy(5), deploy(10, { status: 'failed' }), deploy(9), deploy(40)]
    const m = measure(classify(records), NOW - 30 * DAY, NOW)
    expect(m.deployments).toBe(4)
    expect(m.frequencyPerDay).toBeCloseTo(3 / 30)
    expect(m.leadTimeP50Hours).toBe(6)
    expect(m.leadTimeP90Hours).toBe(6)
    expect(m.changeFailureRate).toBe(0.25)
    expect(m.timeToRestoreP50Hours).toBe(24)
    expect(m.grade).toBe('low')
  })

  it('says nothing where there is nothing', () => {
    const m = measure([], NOW - 7 * DAY, NOW)
    expect(m).toEqual({ deployments: 0, frequencyPerDay: 0, leadTimeP50Hours: null, leadTimeP90Hours: null, changeFailureRate: null, timeToRestoreP50Hours: null, grade: null })
  })

  it('leaves lead time null while commits are unread', () => {
    const m = measure(classify([deploy(1, { commits: null })]), NOW - 7 * DAY, NOW)
    expect(m.leadTimeP50Hours).toBeNull()
    expect(m.deployments).toBe(1)
  })
})

describe('grade', () => {
  it('takes the worst band among the measures with data', () => {
    expect(grade({ deployments: 10, frequencyPerDay: 1.5, leadTimeP50Hours: 3, leadTimeP90Hours: 3, changeFailureRate: 0, timeToRestoreP50Hours: null })).toBe('elite')
    expect(grade({ deployments: 10, frequencyPerDay: 1.5, leadTimeP50Hours: 3, leadTimeP90Hours: 3, changeFailureRate: 0.12, timeToRestoreP50Hours: null })).toBe('medium')
    expect(grade({ deployments: 2, frequencyPerDay: 0.01, leadTimeP50Hours: null, leadTimeP90Hours: null, changeFailureRate: 0, timeToRestoreP50Hours: null })).toBe('low')
    expect(grade({ deployments: 0, frequencyPerDay: 0, leadTimeP50Hours: null, leadTimeP90Hours: null, changeFailureRate: null, timeToRestoreP50Hours: null })).toBeNull()
  })
})

describe('series', () => {
  it('buckets a 7 day window by day, oldest first', () => {
    const s = series(classify([deploy(0.5), deploy(0.6), deploy(6.5)]), NOW - 7 * DAY, NOW, 7)
    expect(s.frequency).toEqual([1, 0, 0, 0, 0, 0, 2])
    expect(s.leadTime).toEqual([6, null, null, null, null, null, 6])
  })

  it('buckets 30 days by three and 90 by seven', () => {
    expect(series([], NOW - 30 * DAY, NOW, 30).frequency).toHaveLength(10)
    expect(series([], NOW - 90 * DAY, NOW, 90).frequency).toHaveLength(13)
  })
})

describe('outcomes', () => {
  const names = (repoId: string, service: string) => (repoId === 'github:1' ? { fullName: 'example/payments', name: service || 'payments' } : null)

  it('rolls up the estate and one row per repository and service, with the previous window beside it', () => {
    const records = [deploy(1), deploy(2, { service: 'api' }), deploy(35), deploy(50, { repoId: 'github:2' })]
    const out = outcomes({ records, names, window: 30, now: NOW })
    expect(out.rows.map((r) => [r.fullName, r.service, r.name, r.current.deployments, r.previous.deployments])).toEqual([
      ['example/payments', '', 'payments', 1, 1],
      ['example/payments', 'api', 'api', 1, 0],
    ])
    expect(out.estate.current.deployments).toBe(2)
    expect(out.estate.previous.deployments).toBe(2)
    expect(out.rows[0]!.series.frequency).toHaveLength(10)
    expect(out.rows[0]!.sources).toEqual(['forge'])
  })

  it('leaves out a row with nothing in either window', () => {
    const out = outcomes({ records: [deploy(100)], names, window: 30, now: NOW })
    expect(out.rows).toEqual([])
  })
})
