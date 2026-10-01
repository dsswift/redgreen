import { describe, expect, it } from 'vitest'
import { checksState, cronDays, definitionSchedules, ianaTimeZone, runStatus, securityCounts, shortRef, toFacts, topLanguages, trigger, type AdoBuild, type AdoDefinition } from './normalize.ts'
import { parseSchedules } from './pipeline-file.ts'

const build = (over: Partial<AdoBuild>): AdoBuild => ({
  id: 1,
  buildNumber: '20260930.1',
  status: 'completed',
  result: 'succeeded',
  queueTime: '2026-09-30T01:00:00Z',
  definition: { id: 7 },
  sourceBranch: 'refs/heads/main',
  sourceVersion: 'abc',
  reason: 'individualCI',
  ...over,
})

describe('azure devops normalize', () => {
  it('shortens branch and tag refs and keeps the rest', () => {
    expect(shortRef('refs/heads/release/1.0')).toBe('release/1.0')
    expect(shortRef('refs/tags/v1.2.0')).toBe('v1.2.0')
    expect(shortRef('refs/pull/12/merge')).toBe('pull/12/merge')
  })

  it('reads a build as a run status', () => {
    expect(runStatus({ status: 'completed', result: 'failed' })).toEqual({ status: 'completed', conclusion: 'failure' })
    expect(runStatus({ status: 'completed', result: 'partiallySucceeded' })).toEqual({ status: 'completed', conclusion: 'success' })
    expect(runStatus({ status: 'completed', result: 'canceled' })).toEqual({ status: 'completed', conclusion: 'cancelled' })
    expect(runStatus({ status: 'inProgress' })).toEqual({ status: 'running', conclusion: null })
    expect(runStatus({ status: 'notStarted' })).toEqual({ status: 'queued', conclusion: null })
  })

  it('tells a tag build from a branch build by its ref', () => {
    expect(trigger({ reason: 'individualCI', sourceBranch: 'refs/tags/v1' })).toBe('tag')
    expect(trigger({ reason: 'batchedCI', sourceBranch: 'refs/heads/main' })).toBe('push')
    expect(trigger({ reason: 'pullRequest', sourceBranch: 'refs/pull/3/merge' })).toBe('pull_request')
    expect(trigger({ reason: 'schedule', sourceBranch: 'refs/heads/main' })).toBe('schedule')
    expect(trigger({ reason: 'manual', sourceBranch: 'refs/heads/main' })).toBe('manual')
    expect(trigger({ reason: 'buildCompletion', sourceBranch: 'refs/heads/main' })).toBe('other')
  })

  it('writes weekday sets as a cron day-of-week field', () => {
    expect(cronDays(31)).toBe('1,2,3,4,5')
    expect(cronDays(64)).toBe('0')
    expect(cronDays(127)).toBe('*')
    expect(cronDays('all')).toBe('*')
    expect(cronDays('monday, friday')).toBe('1,5')
  })

  it('resolves Windows time zone names', () => {
    expect(ianaTimeZone('Central Standard Time')).toBe('America/Chicago')
    expect(ianaTimeZone('UTC')).toBe('Etc/UTC')
    expect(ianaTimeZone('Nowhere Standard Time')).toBe('UTC')
  })

  it('watches editor schedules that always fire and sets aside the ones that wait for a change', () => {
    const schedule = { timeZoneId: 'Central Standard Time', startHours: 5, startMinutes: 30, daysToBuild: 31 }
    const definition = (schedules: object[]): AdoDefinition => ({ id: 1, name: 'Nightly', createdDate: '2026-01-01T00:00:00Z', triggers: [{ triggerType: 'continuousIntegration' }, { triggerType: 'schedule', schedules }] } as AdoDefinition)
    expect(definitionSchedules(definition([schedule, { ...schedule, scheduleOnlyWithChanges: true }]))).toEqual({
      schedules: [{ cron: '30 5 * * 1,2,3,4,5', timezone: 'America/Chicago' }],
      conditional: 1,
    })
    expect(definitionSchedules({ id: 1, name: 'CI', createdDate: '2026-01-01T00:00:00Z', triggers: [{ triggerType: 'continuousIntegration' }] })).toBeNull()
  })

  it('watches only YAML schedules marked always', () => {
    const yaml = `
schedules:
  - cron: "0 2 * * *"
    displayName: Nightly
    always: true
  - cron: "0 6 * * 1"
    branches: { include: [main] }
trigger: [main]
`
    expect(parseSchedules(yaml)).toEqual({ schedules: [{ cron: '0 2 * * *', timezone: 'UTC' }], conditional: 1 })
    expect(parseSchedules('trigger: [main]')).toEqual({ schedules: [], conditional: 0 })
    expect(parseSchedules(': not yaml :\n  - [')).toEqual({ schedules: [], conditional: 0 })
  })

  it('judges a pull request by the newest build of each pipeline', () => {
    expect(checksState([])).toBe('none')
    expect(checksState([build({ result: 'canceled' })])).toBe('none')
    expect(checksState([build({ id: 2, result: 'succeeded', queueTime: '2026-09-30T02:00:00Z' }), build({ id: 1, result: 'failed' })])).toBe('success')
    expect(checksState([build({ result: 'succeeded' }), build({ id: 2, definition: { id: 8 }, result: 'failed' })])).toBe('failure')
    expect(checksState([build({ result: 'succeeded' }), build({ id: 2, definition: { id: 8 }, status: 'inProgress', result: undefined })])).toBe('pending')
  })

  it('splits alerts into the dependency and code feeds', () => {
    const alerts = [
      { alertType: 'dependency', severity: 'critical' },
      { alertType: 'dependency', severity: 'high' },
      { alertType: 'secret', severity: 'critical' },
      { alertType: 'code', severity: 'critical' },
      { alertType: 'code', severity: 'medium' },
    ]
    expect(securityCounts(alerts, 'https://dev.azure.com/acme/Shop/_git/api')).toEqual({
      dependabot: { critical: 1, high: 1, url: 'https://dev.azure.com/acme/Shop/_git/api/alerts' },
      codeScanning: { critical: 2, high: 0, url: 'https://dev.azure.com/acme/Shop/_git/api/alerts' },
    })
  })

  it('picks the dominant programming language and skips data files', () => {
    const metrics = { repositoryLanguageAnalytics: [{ id: 'r1', languageBreakdown: [{ name: '.package' }, { name: 'C#', languagePercentage: 56 }, { name: 'HTML', languagePercentage: 27 }] }, { id: 'r2', languageBreakdown: [] }] }
    expect([...topLanguages(metrics)]).toEqual([
      ['r1', 'C#'],
      ['r2', null],
    ])
  })

  it('reads branch rules from enabled policies', () => {
    const policy = (id: string, over: object = {}) => ({ isEnabled: true, isBlocking: true, type: { id }, ...over })
    const facts = toFacts(
      [
        policy('fa4e907d-c16b-4a4c-9dfa-4906e5d171dd', { settings: { minimumApproverCount: 2 } }),
        policy('fa4e907d-c16b-4a4c-9dfa-4906e5d171dd', { settings: { minimumApproverCount: 5 }, isEnabled: false }),
        policy('fd2167ab-b0be-447a-8ec8-39368250530e', { isBlocking: false }),
      ],
      ['/', '/README.md', '/Dockerfile', '/src'],
      { author: { name: 'A Committer', date: '2026-09-01T00:00:00Z' } },
    )
    expect(facts).toMatchObject({ branchProtected: true, requiresReview: true, requiredApprovals: 2, requiresCodeOwnerReview: false, hasReadme: true, hasDockerfile: true, lastCommitAt: '2026-09-01T00:00:00Z' })
    expect(toFacts(null, null, null)).toMatchObject({ branchProtected: null, requiresReview: null, requiredApprovals: null, hasReadme: null })
    expect(toFacts([], [], null)).toMatchObject({ branchProtected: false, requiresReview: false, requiredApprovals: null, hasReadme: false })
  })
})
