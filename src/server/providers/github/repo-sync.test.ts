import type { Octokit } from 'octokit'
import { describe, expect, it } from 'vitest'
import { createLogger } from '../../log.ts'
import { syncGithubRepo } from './repo-sync.ts'

const REPO = {
  id: 1,
  name: 'mirror',
  full_name: 'acme/mirror',
  html_url: 'https://example.org/acme/mirror',
  description: null,
  default_branch: 'main',
  private: true,
  archived: false,
  fork: false,
  language: null,
  pushed_at: '2026-09-30T02:34:00Z',
  stargazers_count: 0,
  open_issues_count: 0,
  owner: { id: 10, login: 'acme' },
}
const WORKFLOW = {
  id: 7,
  name: 'Pipeline',
  path: '.github/workflows/pipeline.yaml',
  state: 'active',
  html_url: 'https://example.org/wf',
  created_at: '2026-09-30T02:33:59Z',
  updated_at: '2026-09-30T02:33:59Z',
}
const RUN = {
  id: 99,
  run_number: 1,
  workflow_id: 7,
  display_title: 'sync',
  event: 'push',
  head_branch: 'main',
  head_sha: 'abc',
  status: 'completed',
  conclusion: 'failure',
  created_at: '2026-09-30T02:34:08Z',
  updated_at: '2026-09-30T02:35:00Z',
  html_url: 'https://example.org/run',
  actor: null,
}

/** Answers the routes a repo sync reads; `permissions` is the Actions permissions response or an HTTP status to fail with. */
function fakeKit(permissions: { enabled: boolean } | number): Octokit {
  const routes: Record<string, unknown> = {
    'GET /repos/{owner}/{repo}': REPO,
    'GET /repos/{owner}/{repo}/tags': [],
    'GET /repos/{owner}/{repo}/releases': [],
    'GET /repos/{owner}/{repo}/pulls': [],
    'GET /repos/{owner}/{repo}/actions/runs': { workflow_runs: [RUN] },
    'GET /repos/{owner}/{repo}/contents/{path}': { encoding: 'base64', content: Buffer.from('on: push').toString('base64') },
  }
  const pages: Record<string, unknown[]> = {
    'GET /repos/{owner}/{repo}/actions/workflows': [WORKFLOW],
    'GET /repos/{owner}/{repo}/dependabot/alerts': [],
    'GET /repos/{owner}/{repo}/code-scanning/alerts': [],
  }
  const fail = (status: number) => Object.assign(new Error(`HTTP ${status}`), { status })
  return {
    request: async (route: string) => {
      if (route === 'GET /repos/{owner}/{repo}/actions/permissions') {
        if (typeof permissions === 'number') throw fail(permissions)
        return { data: permissions }
      }
      if (!(route in routes)) throw fail(404)
      return { data: routes[route] }
    },
    paginate: async (route: string) => {
      if (!(route in pages)) throw fail(404)
      return pages[route]
    },
  } as unknown as Octokit
}

const log = createLogger('test')

describe('syncGithubRepo', () => {
  it('marks every pipeline disabled when Actions is switched off for the repo', async () => {
    const snapshot = await syncGithubRepo(fakeKit({ enabled: false }), 'github:10', 'acme/mirror', log)
    expect(snapshot.pipelines.map((p) => p.state)).toEqual(['disabled'])
    expect(snapshot.pipelines[0]?.runs).toHaveLength(1)
  })

  it('keeps pipelines enabled when Actions is on', async () => {
    const snapshot = await syncGithubRepo(fakeKit({ enabled: true }), 'github:10', 'acme/mirror', log)
    expect(snapshot.pipelines.map((p) => p.state)).toEqual(['enabled'])
  })

  it('keeps pipelines as GitHub reports them when the Actions setting cannot be read', async () => {
    const snapshot = await syncGithubRepo(fakeKit(403), 'github:10', 'acme/mirror', log)
    expect(snapshot.pipelines.map((p) => p.state)).toEqual(['enabled'])
  })
})
