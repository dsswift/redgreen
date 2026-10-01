import { describe, expect, it } from 'vitest'
import { createLogger } from '../../log.ts'
import type { AzureDevopsClient, Query } from './client.ts'
import { syncAzureDevopsRepo } from './repo-sync.ts'

const ORG = 'https://dev.azure.com/acme'
const PROJECT = `${ORG}/p1`
const GIT = `${PROJECT}/_apis/git/repositories/r1`

const REPO = {
  id: 'r1',
  name: 'api',
  webUrl: 'https://dev.azure.com/acme/Shop/_git/api',
  defaultBranch: 'refs/heads/main',
  isDisabled: false,
  project: { id: 'p1', name: 'Shop', visibility: 'private' },
}
const build = (over: Record<string, unknown>) => ({
  id: 1,
  buildNumber: '20260930.1',
  status: 'completed',
  result: 'succeeded',
  queueTime: '2026-09-30T01:00:00Z',
  finishTime: '2026-09-30T01:05:00Z',
  definition: { id: 7 },
  sourceBranch: 'refs/heads/main',
  sourceVersion: 'abc',
  reason: 'individualCI',
  _links: { web: { href: 'https://dev.azure.com/acme/p1/_build/results?buildId=1' } },
  ...over,
})
const CI = { id: 7, name: 'api-ci', queueStatus: 'enabled', createdDate: '2026-01-01T00:00:00Z', process: { type: 2, yamlFilename: 'azure-pipelines.yml' }, triggers: [{ triggerType: 'continuousIntegration' }] }
const NIGHTLY = {
  id: 8,
  name: 'api-nightly',
  queueStatus: 'paused',
  createdDate: '2026-02-01T00:00:00Z',
  process: { type: 1 },
  triggers: [{ triggerType: 'schedule', schedules: [{ timeZoneId: 'UTC', startHours: 2, startMinutes: 0, daysToBuild: 'all' }] }],
}

/** Answers the URLs a repository sync reads. Builds are told apart by the filter the sync asked with. */
function fakeClient(over: Record<string, unknown> = {}, repo: object = REPO): AzureDevopsClient {
  const routes: Record<string, unknown> = {
    [`${ORG}/_apis/git/repositories/r1`]: repo,
    [`${GIT}/pushes`]: { value: [{ date: '2026-09-30T02:34:00Z' }] },
    [`${GIT}/pullrequests`]: { value: [{ pullRequestId: 11, title: 'Fix', isDraft: false, creationDate: '2026-09-29T00:00:00Z', createdBy: { displayName: 'A Developer' } }] },
    [`${GIT}/commits`]: { value: [{ author: { name: 'A Developer', date: '2026-09-30T02:33:00Z' } }] },
    [`${GIT}/items scopePath=/`]: [{ path: '/' }, { path: '/README.md' }, { path: '/azure-pipelines.yml' }],
    [`${GIT}/items path=/azure-pipelines.yml`]: { content: 'schedules:\n  - cron: "0 4 * * *"\n    always: true\n' },
    [`${PROJECT}/_apis/git/policy/configurations`]: [{ isEnabled: true, isBlocking: true, type: { id: 'fa4e907d-c16b-4a4c-9dfa-4906e5d171dd' }, settings: { minimumApproverCount: 1 } }],
    [`${PROJECT}/_apis/build/definitions`]: [CI, NIGHTLY],
    [`${PROJECT}/_apis/build/builds`]: { value: [build({ id: 3, sourceBranch: 'refs/tags/v1.2.0', result: 'failed', queueTime: '2026-09-30T03:00:00Z' }), build({ id: 1 })] },
    [`${PROJECT}/_apis/build/builds branchName=refs/pull/11/merge`]: { value: [build({ id: 5, reason: 'pullRequest', sourceBranch: 'refs/pull/11/merge', result: 'failed' })] },
    [`${PROJECT}/_apis/build/builds definitions=7 reasonFilter=schedule`]: { value: [build({ id: 2, reason: 'schedule', queueTime: '2026-09-30T04:00:00Z' })] },
    [`${PROJECT}/_apis/build/builds definitions=8 branchName=refs/heads/main`]: { value: [] },
    [`${PROJECT}/_apis/build/builds definitions=8 reasonFilter=schedule`]: { value: [] },
    'https://advsec.dev.azure.com/acme/p1/_apis/alert/repositories/r1/alerts': 403,
    ...over,
  }
  const fail = (status: number, url: string) => Object.assign(new Error(`HTTP ${status} ${url}`), { status })
  const answer = async (url: string, query: Query = {}) => {
    const filters = ['scopePath', 'path', 'definitions', 'branchName', 'reasonFilter'].flatMap((k) => (query[k] === undefined ? [] : [`${k}=${query[k]}`]))
    const key = [url, ...filters].join(' ')
    if (!(key in routes)) throw fail(404, key)
    const found = routes[key]
    if (typeof found === 'number') throw fail(found, key)
    return found
  }
  return { get: answer, list: answer } as unknown as AzureDevopsClient
}

const log = createLogger('test')
const language = async () => 'C#'
const sync = (client: AzureDevopsClient) => syncAzureDevopsRepo(client, 'azuredevops:org:acme', 'acme', 'r1', language, log)

describe('syncAzureDevopsRepo', () => {
  it('builds one pipeline per definition, with schedules from the YAML file or the editor', async () => {
    const snapshot = await sync(fakeClient())
    expect(snapshot).toMatchObject({
      id: 'azuredevops:r1',
      provider: 'azuredevops',
      fullName: 'acme/Shop/api',
      defaultBranch: 'main',
      visibility: 'private',
      language: 'C#',
      pushedAt: '2026-09-30T02:34:00Z',
      openPullRequests: 1,
      security: { dependabot: null, codeScanning: null },
      facts: { branchProtected: true, requiresReview: true, requiredApprovals: 1, hasReadme: true, hasDockerfile: false },
    })
    const [ci, nightly] = snapshot.pipelines
    expect(ci).toMatchObject({ id: '7', name: 'api-ci', path: 'azure-pipelines.yml', state: 'enabled', schedules: [{ cron: '0 4 * * *', timezone: 'UTC' }] })
    expect(ci?.runs.map((r) => [r.id, r.trigger, r.ref, r.conclusion])).toEqual([
      ['2', 'schedule', 'main', 'success'],
      ['3', 'tag', 'v1.2.0', 'failure'],
      ['1', 'push', 'main', 'success'],
    ])
    expect(nightly).toMatchObject({ id: '8', path: 'classic pipeline', state: 'disabled', schedules: [{ cron: '0 2 * * *', timezone: 'Etc/UTC' }], runs: [] })
    expect(snapshot.pullRequests).toEqual([
      { number: 11, title: 'Fix', url: 'https://dev.azure.com/acme/Shop/_git/api/pullrequest/11', author: 'A Developer', draft: false, updatedAt: '2026-09-29T00:00:00Z', checks: 'failure' },
    ])
  })

  it('counts alerts when Advanced Security is on', async () => {
    const snapshot = await sync(fakeClient({ 'https://advsec.dev.azure.com/acme/p1/_apis/alert/repositories/r1/alerts': [{ alertType: 'dependency', severity: 'critical' }] }))
    expect(snapshot.security.dependabot).toMatchObject({ critical: 1, high: 0 })
    expect(snapshot.security.codeScanning).toMatchObject({ critical: 0, high: 0 })
  })

  it('reports no pipelines for a repository nothing builds', async () => {
    const snapshot = await sync(fakeClient({ [`${PROJECT}/_apis/build/definitions`]: [] }))
    expect(snapshot.pipelines).toEqual([])
  })

  it('shows a disabled repository as archived without reading its feeds', async () => {
    const snapshot = await sync(fakeClient({}, { ...REPO, isDisabled: true }))
    expect(snapshot).toMatchObject({ archived: true, pipelines: [], pullRequests: [] })
    expect(snapshot.facts).toBeUndefined()
  })

  it('reads a repository with no commits without asking for its files', async () => {
    const { defaultBranch: _none, ...empty } = REPO
    const snapshot = await sync(fakeClient({ [`${PROJECT}/_apis/build/definitions`]: [], [`${GIT}/pushes`]: { value: [] }, [`${GIT}/pullrequests`]: { value: [] } }, empty))
    expect(snapshot).toMatchObject({ defaultBranch: 'main', pushedAt: null, facts: { hasReadme: null, branchProtected: null } })
  })
})
