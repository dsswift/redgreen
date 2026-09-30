import { describe, expect, it } from 'vitest'
import { checksState, ianaTimeZone, runStatus, securityCounts, toRun } from './normalize.ts'
import { topLevel } from './provider.ts'

describe('gitlab normalize', () => {
  it('maps pipeline statuses onto run status and conclusion', () => {
    expect(runStatus('success')).toEqual({ status: 'completed', conclusion: 'success' })
    expect(runStatus('failed')).toEqual({ status: 'completed', conclusion: 'failure' })
    expect(runStatus('canceled')).toEqual({ status: 'completed', conclusion: 'cancelled' })
    expect(runStatus('manual')).toEqual({ status: 'completed', conclusion: 'action_required' })
    expect(runStatus('running')).toEqual({ status: 'running', conclusion: null })
    expect(runStatus('pending')).toEqual({ status: 'queued', conclusion: null })
    expect(runStatus('waiting_for_resource')).toEqual({ status: 'queued', conclusion: null })
  })

  it('tells tag pushes from branch pushes by the tag list', () => {
    const base = { id: 1, iid: 1, name: null, sha: 'a', status: 'success', created_at: '2026-09-30T00:00:00Z', updated_at: '2026-09-30T00:00:00Z', web_url: 'u' }
    const tags = new Set(['v1.0.0'])
    expect(toRun({ ...base, ref: 'v1.0.0', source: 'push' }, tags).trigger).toBe('tag')
    expect(toRun({ ...base, ref: 'main', source: 'push' }, tags).trigger).toBe('push')
    expect(toRun({ ...base, ref: 'main', source: 'schedule' }, tags).trigger).toBe('schedule')
    expect(toRun({ ...base, ref: 'main', source: 'web' }, tags).trigger).toBe('manual')
    expect(toRun({ ...base, ref: 'feature', source: 'merge_request_event' }, tags).trigger).toBe('pull_request')
    expect(toRun({ ...base, ref: 'main', source: 'api' }, tags).trigger).toBe('other')
  })

  it('reads a head pipeline as a checks state', () => {
    expect(checksState('success')).toBe('success')
    expect(checksState('failed')).toBe('failure')
    expect(checksState('running')).toBe('pending')
    expect(checksState('manual')).toBe('pending')
    expect(checksState('canceled')).toBe('none')
    expect(checksState(null)).toBe('none')
  })

  it('resolves schedule time zones to IANA names and falls back to UTC', () => {
    expect(ianaTimeZone('America/New_York')).toBe('America/New_York')
    expect(ianaTimeZone('Pacific Time (US & Canada)')).toBe('America/Los_Angeles')
    expect(ianaTimeZone('UTC')).toBe('UTC')
    expect(ianaTimeZone('Nowhere/Special')).toBe('UTC')
  })

  it('reports a security feed only when a scanner of that kind is enabled and counts are visible', () => {
    const url = 'https://example.org/g/p'
    expect(securityCounts(null, url)).toEqual({ dependabot: null, codeScanning: null })
    const security = {
      securityScanners: { enabled: ['SAST'] },
      dependency: { critical: 2, high: 1 },
      code: { critical: 0, high: 3 },
    }
    expect(securityCounts(security, url)).toEqual({
      dependabot: null,
      codeScanning: { critical: 0, high: 3, url: `${url}/-/security/vulnerability_report` },
    })
    expect(securityCounts({ ...security, code: null }, url).codeScanning).toBeNull()
  })

  it('keeps only groups not nested under another discovered group', () => {
    const group = (full_path: string, id: number) => ({ id, name: full_path, path: full_path, full_path, parent_id: null, avatar_url: null, web_url: '' })
    const kept = topLevel([group('acme', 1), group('acme/platform', 2), group('other', 3), group('acme-labs', 4)])
    expect(kept.map((g) => g.full_path)).toEqual(['acme', 'other', 'acme-labs'])
  })
})
