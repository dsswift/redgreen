import type { Pipeline, PullRequest, Release, RepoSnapshot, Run } from '../../../shared/model.ts'
import type { Logger } from '../../log.ts'
import { isStatus } from '../errors.ts'
import type { GitlabClient } from './client.ts'
import {
  checksState,
  securityCounts,
  toMainPipeline,
  toRun,
  toSchedulePipeline,
  topLanguage,
  type GlMergeRequest,
  type GlPipeline,
  type GlProject,
  type GlRelease,
  type GlSchedule,
  type GlSecurity,
} from './normalize.ts'

/** Runs kept per pipeline. Enough to see a failing streak and a sparkline. */
const RUNS_PER_PIPELINE = 30
/** Open merge requests whose head pipeline is looked up. The rest are counted only. */
const MR_CHECKS_LIMIT = 20

const SECURITY_QUERY = `
query ($path: ID!) {
  project(fullPath: $path) {
    securityScanners { enabled }
    dependency: vulnerabilitySeveritiesCount(state: [DETECTED, CONFIRMED], reportType: [DEPENDENCY_SCANNING, CONTAINER_SCANNING]) { critical high }
    code: vulnerabilitySeveritiesCount(state: [DETECTED, CONFIRMED], reportType: [SAST, SECRET_DETECTION, DAST, API_FUZZING, COVERAGE_FUZZING]) { critical high }
  }
}`

/** Reads one project by its numeric id, so a rename between sweeps still finds it. */
export async function syncGitlabRepo(client: GitlabClient, accountId: string, projectId: number, log: Logger): Promise<RepoSnapshot> {
  const project = await client.get<GlProject>(`projects/${projectId}`)
  const fullName = project.path_with_namespace
  const base: RepoSnapshot = {
    id: `gitlab:${project.id}`,
    provider: 'gitlab',
    accountId,
    name: project.name,
    fullName,
    url: project.web_url,
    description: project.description,
    defaultBranch: project.default_branch ?? 'main',
    visibility: project.visibility,
    archived: project.archived,
    fork: Boolean(project.forked_from_project),
    language: null,
    pushedAt: project.last_activity_at,
    stars: project.star_count,
    openIssues: project.open_issues_count ?? 0,
    openPullRequests: 0,
    pipelines: [],
    releases: [],
    pullRequests: [],
    security: { dependabot: null, codeScanning: null },
    syncedAt: new Date().toISOString(),
  }
  if (project.archived) {
    log.debug('archived project; skipping activity feeds', { repo: fullName })
    return base
  }

  const [language, tags, releases, mergeRequests, security] = await Promise.all([
    fetchLanguage(client, project.id),
    fetchTags(client, project.id),
    fetchReleases(client, project.id),
    fetchMergeRequests(client, project.id),
    fetchSecurity(client, fullName, log),
  ])
  const pipelines = await fetchPipelines(client, project, tags, log)
  const pullRequests = await withChecks(client, project.id, mergeRequests)

  return {
    ...base,
    language,
    openPullRequests: mergeRequests.length,
    pipelines,
    releases,
    pullRequests,
    security: securityCounts(security, project.web_url),
  }
}

async function fetchLanguage(client: GitlabClient, projectId: number): Promise<string | null> {
  return topLanguage(await client.get<Record<string, number>>(`projects/${projectId}/languages`))
}

async function fetchTags(client: GitlabClient, projectId: number): Promise<Set<string>> {
  const tags = await client.get<{ name: string }[]>(`projects/${projectId}/repository/tags`, { per_page: 30 })
  return new Set(tags.map((t) => t.name))
}

async function fetchReleases(client: GitlabClient, projectId: number): Promise<Release[]> {
  const releases = await client.get<GlRelease[]>(`projects/${projectId}/releases`, { per_page: 5 })
  return releases.map((r) => ({
    tag: r.tag_name,
    name: r.name ?? r.tag_name,
    publishedAt: r.released_at,
    url: r._links.self,
    prerelease: r.upcoming_release,
  }))
}

async function fetchMergeRequests(client: GitlabClient, projectId: number): Promise<GlMergeRequest[]> {
  return client.get<GlMergeRequest[]>(`projects/${projectId}/merge_requests`, { state: 'opened', order_by: 'updated_at', sort: 'desc', per_page: 100 })
}

/** The head pipeline is only on the single merge request view, so it costs one call per request. */
async function withChecks(client: GitlabClient, projectId: number, mergeRequests: GlMergeRequest[]): Promise<PullRequest[]> {
  const looked = mergeRequests.slice(0, MR_CHECKS_LIMIT)
  const states = await Promise.all(
    looked.map(async (mr) => {
      const detail = await client.get<{ head_pipeline: { status: string } | null }>(`projects/${projectId}/merge_requests/${mr.iid}`)
      return checksState(detail.head_pipeline?.status)
    }),
  )
  return looked.map((mr, i) => ({
    number: mr.iid,
    title: mr.title,
    url: mr.web_url,
    author: mr.author?.username ?? null,
    draft: mr.draft,
    updatedAt: mr.updated_at,
    checks: states[i] ?? 'none',
  }))
}

/** Vulnerability counts need Ultimate; on any other tier or on CE the query answers null or errors, and both feeds stay unavailable. */
async function fetchSecurity(client: GitlabClient, fullName: string, log: Logger): Promise<GlSecurity | null> {
  const { data, errors } = await client.graphql<{ project: GlSecurity | null }>(SECURITY_QUERY, { path: fullName })
  if (errors.length > 0 || !data?.project) {
    log.debug('security counts unavailable', { repo: fullName, errors: errors.map((e) => e.message) })
    return null
  }
  return data.project
}

async function fetchPipelines(client: GitlabClient, project: GlProject, tags: Set<string>, log: Logger): Promise<Pipeline[]> {
  const projectId = project.id
  let recent: GlPipeline[]
  let schedules: GlSchedule[]
  try {
    ;[recent, schedules] = await Promise.all([
      client.get<GlPipeline[]>(`projects/${projectId}/pipelines`, { per_page: 100 }),
      client.get<GlSchedule[]>(`projects/${projectId}/pipeline_schedules`, { per_page: 100 }),
    ])
  } catch (error) {
    if (isStatus(error, 403, 404)) {
      log.debug('ci unavailable', { repo: project.path_with_namespace, status: Number((error as { status: number }).status) })
      return []
    }
    throw error
  }
  if (recent.length === 0 && schedules.length === 0) return []

  // Nothing here reports "disabled_inactivity" like GitHub does; a project either runs CI or has it switched off.
  const ciDisabled = project.builds_access_level === 'disabled'
  if (ciDisabled) log.info('ci switched off for project; pipelines marked disabled', { repo: project.path_with_namespace })
  const defaultBranch = project.default_branch ?? 'main'
  const isScheduled = (p: GlPipeline) => p.source === 'schedule' || p.source === 'scheduled'
  const onDefault = (p: GlPipeline) => p.ref === defaultBranch && !isScheduled(p) && p.source !== 'merge_request_event' && p.source !== 'external_pull_request_event'
  const decisive = (p: GlPipeline) => p.status === 'success' || p.status === 'failed'

  const runs = new Map<number, GlPipeline>(recent.filter((p) => !isScheduled(p)).map((p) => [p.id, p]))
  if (![...runs.values()].some((p) => onDefault(p) && decisive(p))) {
    const page = await client.get<GlPipeline[]>(`projects/${projectId}/pipelines`, { ref: defaultBranch, per_page: 10 })
    for (const p of page.filter((p) => !isScheduled(p))) runs.set(p.id, p)
  }
  const main = toMainPipeline(project, ciDisabled ? 'disabled' : 'enabled', order(runs.values(), tags))

  const scheduled = await Promise.all(
    schedules.map(async (schedule) => {
      const pipelines = await fetchScheduleRuns(client, projectId, schedule.id, log)
      return toSchedulePipeline(project, schedule, ciDisabled, order(pipelines, tags))
    }),
  )
  return [main, ...scheduled]
}

/** Runs one schedule fired. Older GitLab releases lack the endpoint; the schedule then shows no runs and is judged from its own clock. */
async function fetchScheduleRuns(client: GitlabClient, projectId: number, scheduleId: number, log: Logger): Promise<GlPipeline[]> {
  try {
    return await client.get<GlPipeline[]>(`projects/${projectId}/pipeline_schedules/${scheduleId}/pipelines`, { per_page: RUNS_PER_PIPELINE, sort: 'desc' })
  } catch (error) {
    if (isStatus(error, 404)) {
      log.info('schedule runs unavailable on this gitlab; schedule shows no runs', { project: projectId, schedule: scheduleId })
      return []
    }
    throw error
  }
}

function order(pipelines: Iterable<GlPipeline>, tags: Set<string>): Run[] {
  return [...pipelines]
    .sort((a, b) => b.created_at.localeCompare(a.created_at))
    .slice(0, RUNS_PER_PIPELINE)
    .map((p) => toRun(p, tags))
}
