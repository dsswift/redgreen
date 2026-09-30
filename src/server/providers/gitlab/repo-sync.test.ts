import { describe, expect, it } from 'vitest'
import { createLogger } from '../../log.ts'
import type { GitlabClient } from './client.ts'
import { syncGitlabRepo } from './repo-sync.ts'

const PROJECT = {
  id: 42,
  name: 'api',
  path_with_namespace: 'acme/platform/api',
  web_url: 'https://gitlab.example.org/acme/platform/api',
  description: null,
  default_branch: 'main',
  visibility: 'private',
  archived: false,
  forked_from_project: null,
  star_count: 3,
  open_issues_count: 7,
  last_activity_at: '2026-09-30T02:34:00Z',
  created_at: '2025-01-01T00:00:00Z',
  builds_access_level: 'enabled',
  ci_config_path: '',
  namespace: { id: 9, name: 'Platform', path: 'platform', full_path: 'acme/platform', kind: 'group', parent_id: 1, avatar_url: null, web_url: '' },
}
const pipeline = (over: Partial<{ id: number; ref: string; status: string; source: string; created_at: string }>) => ({
  id: 1,
  iid: 1,
  name: null,
  ref: 'main',
  sha: 'abc',
  status: 'success',
  source: 'push',
  created_at: '2026-09-30T01:00:00Z',
  updated_at: '2026-09-30T01:05:00Z',
  web_url: 'https://gitlab.example.org/acme/platform/api/-/pipelines/1',
  ...over,
})
const SCHEDULE = {
  id: 5,
  description: 'Nightly',
  ref: 'main',
  cron: '0 2 * * *',
  cron_timezone: 'Europe/London',
  active: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

/** Answers the routes a project sync reads. Paths are relative to /api/v4 with the query string dropped. */
function fakeClient(over: Record<string, unknown> = {}, project = PROJECT): GitlabClient {
  const routes: Record<string, unknown> = {
    'projects/42': project,
    'projects/42/languages': { Go: 80.5, Shell: 19.5 },
    'projects/42/repository/tags': [{ name: 'v1.2.0' }],
    'projects/42/releases': [{ tag_name: 'v1.2.0', name: 'v1.2.0', released_at: '2026-09-29T00:00:00Z', upcoming_release: false, _links: { self: 'https://gitlab.example.org/r' } }],
    'projects/42/merge_requests': [{ iid: 11, title: 'Fix', web_url: 'https://gitlab.example.org/mr/11', draft: false, updated_at: '2026-09-30T00:00:00Z', author: { username: 'dev' } }],
    'projects/42/merge_requests/11': { head_pipeline: { status: 'failed' } },
    'projects/42/pipelines': [
      pipeline({ id: 3, ref: 'v1.2.0', status: 'failed', created_at: '2026-09-30T03:00:00Z' }),
      pipeline({ id: 2, source: 'schedule', created_at: '2026-09-30T02:00:00Z' }),
      pipeline({ id: 1 }),
    ],
    'projects/42/pipeline_schedules': [SCHEDULE],
    'projects/42/pipeline_schedules/5/pipelines': [pipeline({ id: 2, source: 'schedule', created_at: '2026-09-30T02:00:00Z' })],
    ...over,
  }
  const fail = (status: number, path: string) => Object.assign(new Error(`HTTP ${status} ${path}`), { status })
  return {
    get: async (path: string) => {
      if (!(path in routes)) throw fail(404, path)
      const answer = routes[path]
      if (typeof answer === 'number') throw fail(answer, path)
      return answer
    },
    graphql: async () => ({
      data: { project: { securityScanners: { enabled: ['DEPENDENCY_SCANNING'] }, dependency: { critical: 1, high: 0 }, code: { critical: 4, high: 0 } } },
      errors: [],
    }),
  } as unknown as GitlabClient
}

const log = createLogger('test')

describe('syncGitlabRepo', () => {
  it('builds one main pipeline plus one per schedule, with scheduled runs only on the schedule', async () => {
    const snapshot = await syncGitlabRepo(fakeClient(), 'gitlab:group:9', 42, log)
    expect(snapshot).toMatchObject({
      id: 'gitlab:42',
      provider: 'gitlab',
      fullName: 'acme/platform/api',
      language: 'Go',
      openIssues: 7,
      openPullRequests: 1,
      security: { dependabot: { critical: 1, high: 0 }, codeScanning: null },
    })
    expect(snapshot.pipelines.map((p) => p.id)).toEqual(['ci', 'schedule:5'])
    const [main, nightly] = snapshot.pipelines
    expect(main?.path).toBe('.gitlab-ci.yml')
    expect(main?.runs.map((r) => [r.id, r.trigger])).toEqual([
      ['3', 'tag'],
      ['1', 'push'],
    ])
    expect(nightly).toMatchObject({ name: 'Nightly', state: 'enabled', schedules: [{ cron: '0 2 * * *', timezone: 'Europe/London' }] })
    expect(nightly?.runs.map((r) => r.trigger)).toEqual(['schedule'])
    expect(snapshot.pullRequests[0]).toMatchObject({ number: 11, checks: 'failure', author: 'dev' })
  })

  it('marks every pipeline disabled when CI is switched off for the project', async () => {
    const snapshot = await syncGitlabRepo(fakeClient({}, { ...PROJECT, builds_access_level: 'disabled' }), 'gitlab:group:9', 42, log)
    expect(snapshot.pipelines.map((p) => p.state)).toEqual(['disabled', 'disabled'])
  })

  it('reports no pipelines for a project that never ran CI', async () => {
    const snapshot = await syncGitlabRepo(fakeClient({ 'projects/42/pipelines': [], 'projects/42/pipeline_schedules': [] }), 'gitlab:group:9', 42, log)
    expect(snapshot.pipelines).toEqual([])
  })

  it('shows a schedule without runs when this gitlab cannot list them', async () => {
    const snapshot = await syncGitlabRepo(fakeClient({ 'projects/42/pipeline_schedules/5/pipelines': 404 }), 'gitlab:group:9', 42, log)
    expect(snapshot.pipelines[1]?.runs).toEqual([])
  })

  it('fetches a default-branch page when the recent pipelines hold no decisive run on it', async () => {
    const snapshot = await syncGitlabRepo(
      fakeClient({ 'projects/42/pipelines': [pipeline({ id: 8, ref: 'feature', status: 'running' })] }),
      'gitlab:group:9',
      42,
      log,
    )
    expect(snapshot.pipelines[0]?.runs.map((r) => r.id)).toEqual(['8'])
  })

  it('skips activity feeds for an archived project', async () => {
    const snapshot = await syncGitlabRepo(fakeClient({ 'projects/42/languages': 500 }, { ...PROJECT, archived: true }), 'gitlab:group:9', 42, log)
    expect(snapshot).toMatchObject({ archived: true, pipelines: [], language: null })
  })
})
