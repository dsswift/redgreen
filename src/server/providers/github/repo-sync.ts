import type { Octokit } from 'octokit'
import type { Pipeline, PullRequest, Release, RepoSnapshot, Run } from '../../../shared/model.ts'
import type { Logger } from '../../log.ts'
import { isStatus } from './client.ts'
import {
  checksState,
  codeScanningCounts,
  dependabotCounts,
  pipelineState,
  toPipeline,
  toRun,
  visibility,
  type GhCheckRun,
  type GhCodeScanningAlert,
  type GhCombinedStatus,
  type GhDependabotAlert,
  type GhPull,
  type GhRelease,
  type GhRepo,
  type GhRun,
  type GhWorkflow,
} from './normalize.ts'
import { parseSchedules } from './workflow-file.ts'

/** Runs kept per pipeline. Enough to see a failing streak and a sparkline. */
const RUNS_PER_PIPELINE = 30
/** Open pull requests whose checks are looked up. The rest are counted only. */
const PR_CHECKS_LIMIT = 20

export async function syncGithubRepo(kit: Octokit, accountId: string, fullName: string, log: Logger): Promise<RepoSnapshot> {
  const [owner, name] = splitFullName(fullName)
  const params = { owner, repo: name }
  const { data: repo } = await kit.request('GET /repos/{owner}/{repo}', params)
  const gh = repo as GhRepo
  const base: RepoSnapshot = {
    id: `github:${gh.id}`,
    provider: 'github',
    accountId,
    name: gh.name,
    fullName: gh.full_name,
    url: gh.html_url,
    description: gh.description,
    defaultBranch: gh.default_branch,
    visibility: visibility(gh),
    archived: gh.archived,
    fork: gh.fork,
    language: gh.language,
    pushedAt: gh.pushed_at,
    stars: gh.stargazers_count,
    openIssues: gh.open_issues_count,
    openPullRequests: 0,
    pipelines: [],
    releases: [],
    pullRequests: [],
    security: { dependabot: null, codeScanning: null },
    syncedAt: new Date().toISOString(),
  }
  if (gh.archived) {
    log.debug('archived repo; skipping activity feeds', { repo: fullName })
    return base
  }

  const [tags, releases, pulls, dependabot, codeScanning] = await Promise.all([
    fetchTags(kit, params),
    fetchReleases(kit, params),
    fetchPulls(kit, params),
    fetchDependabot(kit, params, gh.html_url, log),
    fetchCodeScanning(kit, params, gh.html_url, log),
  ])
  const pipelines = await fetchPipelines(kit, params, gh.default_branch, tags, log)
  const pullRequests = await withChecks(kit, params, pulls)

  return {
    ...base,
    openIssues: Math.max(0, gh.open_issues_count - pulls.length),
    openPullRequests: pulls.length,
    pipelines,
    releases,
    pullRequests,
    security: { dependabot, codeScanning },
  }
}

type RepoParams = { owner: string; repo: string }

function splitFullName(fullName: string): [string, string] {
  const slash = fullName.indexOf('/')
  if (slash <= 0) throw new Error(`Not an owner/name pair: ${fullName}`)
  return [fullName.slice(0, slash), fullName.slice(slash + 1)]
}

async function fetchTags(kit: Octokit, params: RepoParams): Promise<Set<string>> {
  const { data } = await kit.request('GET /repos/{owner}/{repo}/tags', { ...params, per_page: 30 })
  return new Set((data as { name: string }[]).map((t) => t.name))
}

async function fetchReleases(kit: Octokit, params: RepoParams): Promise<Release[]> {
  const { data } = await kit.request('GET /repos/{owner}/{repo}/releases', { ...params, per_page: 5 })
  return (data as GhRelease[])
    .filter((r) => !r.draft && r.published_at !== null)
    .map((r) => ({ tag: r.tag_name, name: r.name ?? r.tag_name, publishedAt: r.published_at!, url: r.html_url, prerelease: r.prerelease }))
}

async function fetchPulls(kit: Octokit, params: RepoParams): Promise<GhPull[]> {
  const { data } = await kit.request('GET /repos/{owner}/{repo}/pulls', {
    ...params,
    state: 'open',
    sort: 'updated',
    direction: 'desc',
    per_page: 100,
  })
  return data as GhPull[]
}

async function withChecks(kit: Octokit, params: RepoParams, pulls: GhPull[]): Promise<PullRequest[]> {
  const looked = pulls.slice(0, PR_CHECKS_LIMIT)
  const states = await Promise.all(
    looked.map(async (pr) => {
      const ref = pr.head.sha
      const [checks, status] = await Promise.all([
        kit.request('GET /repos/{owner}/{repo}/commits/{ref}/check-runs', { ...params, ref, per_page: 100 }),
        kit.request('GET /repos/{owner}/{repo}/commits/{ref}/status', { ...params, ref }),
      ])
      return checksState((checks.data as { check_runs: GhCheckRun[] }).check_runs, status.data as GhCombinedStatus)
    }),
  )
  return looked.map((pr, i) => ({
    number: pr.number,
    title: pr.title,
    url: pr.html_url,
    author: pr.user?.login ?? null,
    draft: pr.draft,
    updatedAt: pr.updated_at,
    checks: states[i] ?? 'none',
  }))
}

async function fetchDependabot(kit: Octokit, params: RepoParams, repoUrl: string, log: Logger) {
  try {
    const alerts = await kit.paginate('GET /repos/{owner}/{repo}/dependabot/alerts', {
      ...params,
      state: 'open',
      severity: 'critical,high',
      per_page: 100,
    })
    return dependabotCounts(alerts as GhDependabotAlert[], `${repoUrl}/security/dependabot`)
  } catch (error) {
    if (isStatus(error, 403, 404)) {
      log.debug('dependabot alerts unavailable', { repo: params.repo, status: Number((error as { status: number }).status) })
      return null
    }
    throw error
  }
}

async function fetchCodeScanning(kit: Octokit, params: RepoParams, repoUrl: string, log: Logger) {
  try {
    const alerts = await kit.paginate('GET /repos/{owner}/{repo}/code-scanning/alerts', { ...params, state: 'open', per_page: 100 })
    return codeScanningCounts(alerts as GhCodeScanningAlert[], `${repoUrl}/security/code-scanning`)
  } catch (error) {
    if (isStatus(error, 403, 404)) {
      log.debug('code scanning unavailable', { repo: params.repo, status: Number((error as { status: number }).status) })
      return null
    }
    throw error
  }
}

async function fetchPipelines(kit: Octokit, params: RepoParams, defaultBranch: string, tags: Set<string>, log: Logger): Promise<Pipeline[]> {
  let workflows: GhWorkflow[]
  try {
    workflows = (await kit.paginate('GET /repos/{owner}/{repo}/actions/workflows', { ...params, per_page: 100 })) as GhWorkflow[]
  } catch (error) {
    if (isStatus(error, 403, 404)) {
      log.debug('actions unavailable', { repo: params.repo })
      return []
    }
    throw error
  }
  const live = workflows.flatMap((w) => {
    const state = pipelineState(w.state)
    return state ? [{ workflow: w, state }] : []
  })
  if (live.length === 0) return []

  // One page of recent runs covers most pipelines. The gaps are filled per pipeline below.
  const { data: recent } = await kit.request('GET /repos/{owner}/{repo}/actions/runs', { ...params, per_page: 100 })
  const recentRuns = (recent as { workflow_runs: GhRun[] }).workflow_runs

  return Promise.all(
    live.map(async ({ workflow, state }) => {
      const schedules = await fetchSchedules(kit, params, workflow, defaultBranch)
      const runs = new Map<number, GhRun>(recentRuns.filter((r) => r.workflow_id === workflow.id).map((r) => [r.id, r]))
      const onDefault = (r: GhRun) => r.head_branch === defaultBranch && r.event !== 'pull_request' && r.event !== 'pull_request_target'
      const decisive = (r: GhRun) => r.status === 'completed' && r.conclusion !== 'cancelled' && r.conclusion !== 'skipped'

      if (![...runs.values()].some((r) => onDefault(r) && decisive(r))) {
        for (const r of await fetchWorkflowRuns(kit, params, workflow.id, { branch: defaultBranch, exclude_pull_requests: true, per_page: 10 })) {
          runs.set(r.id, r)
        }
      }
      if (schedules.length > 0 && ![...runs.values()].some((r) => r.event === 'schedule')) {
        for (const r of await fetchWorkflowRuns(kit, params, workflow.id, { event: 'schedule', per_page: 1 })) runs.set(r.id, r)
      }

      const ordered: Run[] = [...runs.values()]
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .slice(0, RUNS_PER_PIPELINE)
        .map((r) => toRun(r, tags))
      return toPipeline(workflow, state, schedules, ordered)
    }),
  )
}

async function fetchWorkflowRuns(
  kit: Octokit,
  params: RepoParams,
  workflowId: number,
  query: { branch?: string; event?: string; exclude_pull_requests?: boolean; per_page: number },
): Promise<GhRun[]> {
  const { data } = await kit.request('GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs', {
    ...params,
    workflow_id: workflowId,
    ...query,
  })
  return (data as { workflow_runs: GhRun[] }).workflow_runs
}

async function fetchSchedules(kit: Octokit, params: RepoParams, workflow: GhWorkflow, ref: string): Promise<string[]> {
  // Dynamic workflows (e.g. from a marketplace app) have no file in the repo.
  if (!workflow.path.startsWith('.github/workflows/')) return []
  try {
    const { data } = await kit.request('GET /repos/{owner}/{repo}/contents/{path}', { ...params, path: workflow.path, ref })
    const file = data as { content?: string; encoding?: string }
    if (file.encoding !== 'base64' || !file.content) return []
    return parseSchedules(Buffer.from(file.content, 'base64').toString('utf8'))
  } catch (error) {
    if (isStatus(error, 404)) return []
    throw error
  }
}
