import type { AlertCounts, Pipeline, PullRequest, RepoFacts, RepoSnapshot, Run, Schedule } from '../../../shared/model.ts'
import type { Logger } from '../../log.ts'
import { isStatus } from '../errors.ts'
import type { AzureDevopsClient } from './client.ts'
import {
  checksState,
  definitionSchedules,
  securityCounts,
  shortRef,
  toFacts,
  toPipeline,
  toRun,
  visibility,
  type AdoAlert,
  type AdoBuild,
  type AdoCommit,
  type AdoDefinition,
  type AdoPolicy,
  type AdoPullRequest,
  type AdoRepo,
} from './normalize.ts'
import { parseSchedules } from './pipeline-file.ts'

/** Runs kept per pipeline. Enough to see a failing streak and a sparkline. */
const RUNS_PER_PIPELINE = 30
/** Open pull requests whose builds are looked up. The rest are counted only. */
const PR_CHECKS_LIMIT = 20

export const AZURE_DEVOPS_URL = 'https://dev.azure.com'
const ADVANCED_SECURITY_URL = 'https://advsec.dev.azure.com'

/** Dominant language of a repository, or null. Supplied by the provider, which reads it once per project. */
export type LanguageLookup = (projectId: string, repoId: string) => Promise<string | null>

/** Where one repository's resources live. */
interface Scope {
  org: string
  repo: AdoRepo
  /** Project-level APIs. */
  project: string
  /** This repository's git APIs. */
  git: string
  /** Short name of the default branch, or null for a repository with no commits. */
  branch: string | null
}

/** Reads one repository by its id, so a rename between sweeps still finds it. */
export async function syncAzureDevopsRepo(client: AzureDevopsClient, accountId: string, org: string, repoId: string, language: LanguageLookup, log: Logger): Promise<RepoSnapshot> {
  const orgUrl = `${AZURE_DEVOPS_URL}/${encodeURIComponent(org)}`
  const repo = await client.get<AdoRepo>(`${orgUrl}/_apis/git/repositories/${repoId}`)
  const project = `${orgUrl}/${repo.project.id}`
  const scope: Scope = { org, repo, project, git: `${project}/_apis/git/repositories/${repo.id}`, branch: repo.defaultBranch ? shortRef(repo.defaultBranch) : null }
  const fullName = `${org}/${repo.project.name}/${repo.name}`
  const base: RepoSnapshot = {
    id: `azuredevops:${repo.id}`,
    provider: 'azuredevops',
    accountId,
    name: repo.name,
    fullName,
    url: repo.webUrl,
    description: null,
    defaultBranch: scope.branch ?? 'main',
    visibility: visibility(repo),
    archived: Boolean(repo.isDisabled),
    fork: Boolean(repo.isFork),
    language: null,
    pushedAt: null,
    stars: 0,
    openIssues: 0,
    openPullRequests: 0,
    pipelines: [],
    releases: [],
    pullRequests: [],
    security: { dependabot: null, codeScanning: null },
    syncedAt: new Date().toISOString(),
  }
  if (repo.isDisabled) {
    log.debug('disabled repository; skipping activity feeds', { repo: fullName })
    return base
  }

  const [topLanguage, pushedAt, pulls, security, facts, pipelines] = await Promise.all([
    language(repo.project.id, repo.id),
    fetchPushedAt(client, scope),
    fetchPulls(client, scope),
    fetchSecurity(client, scope, log),
    fetchFacts(client, scope, log),
    fetchPipelines(client, scope, log),
  ])
  const pullRequests = await withChecks(client, scope, pulls)

  return { ...base, language: topLanguage, pushedAt, openPullRequests: pulls.length, pipelines, pullRequests, security, facts }
}

/** Runs `fn`, answering null when the application may not read the resource or it does not exist. */
async function tolerate<T>(what: string, scope: Scope, log: Logger, fn: () => Promise<T>): Promise<T | null> {
  try {
    return await fn()
  } catch (error) {
    if (isStatus(error, 403, 404)) {
      log.debug('feed unavailable', { repo: scope.repo.name, what, status: Number((error as { status: number }).status) })
      return null
    }
    throw error
  }
}

async function fetchPushedAt(client: AzureDevopsClient, scope: Scope): Promise<string | null> {
  const [latest] = (await client.get<{ value: { date: string }[] }>(`${scope.git}/pushes`, { $top: 1 })).value
  return latest?.date ?? null
}

async function fetchPulls(client: AzureDevopsClient, scope: Scope): Promise<AdoPullRequest[]> {
  return (await client.get<{ value: AdoPullRequest[] }>(`${scope.git}/pullrequests`, { 'searchCriteria.status': 'active', $top: 100 })).value
}

function buildsQuery(scope: Scope, query: Record<string, string | number>) {
  return { repositoryId: scope.repo.id, repositoryType: 'TfsGit', queryOrder: 'queueTimeDescending', ...query }
}

async function fetchBuilds(client: AzureDevopsClient, scope: Scope, query: Record<string, string | number>): Promise<AdoBuild[]> {
  return (await client.get<{ value: AdoBuild[] }>(`${scope.project}/_apis/build/builds`, buildsQuery(scope, query))).value
}

/** A pull request is built on its merge ref; those builds are its checks. One call per request. */
async function withChecks(client: AzureDevopsClient, scope: Scope, pulls: AdoPullRequest[]): Promise<PullRequest[]> {
  const looked = pulls.slice(0, PR_CHECKS_LIMIT)
  const states = await Promise.all(
    looked.map(async (pr) => {
      try {
        return checksState(await fetchBuilds(client, scope, { branchName: `refs/pull/${pr.pullRequestId}/merge`, $top: 50 }))
      } catch (error) {
        if (isStatus(error, 403, 404)) return 'none' as const
        throw error
      }
    }),
  )
  return looked.map((pr, i) => ({
    number: pr.pullRequestId,
    title: pr.title,
    url: `${scope.repo.webUrl}/pullrequest/${pr.pullRequestId}`,
    author: pr.createdBy?.displayName ?? null,
    draft: Boolean(pr.isDraft),
    // The list carries no time of last change.
    updatedAt: pr.creationDate,
    checks: states[i] ?? 'none',
  }))
}

/** Alerts need Advanced Security switched on for the repository; without it both feeds stay unavailable. */
async function fetchSecurity(client: AzureDevopsClient, scope: Scope, log: Logger): Promise<{ dependabot: AlertCounts | null; codeScanning: AlertCounts | null }> {
  const url = `${ADVANCED_SECURITY_URL}/${encodeURIComponent(scope.org)}/${scope.repo.project.id}/_apis/alert/repositories/${scope.repo.id}/alerts`
  try {
    const alerts = await client.list<AdoAlert>(url, { 'criteria.states': 'active', top: 500, 'api-version': '7.2-preview.1' })
    return securityCounts(alerts, scope.repo.webUrl)
  } catch (error) {
    const notEnabled = isStatus(error, 400) && error instanceof Error && error.message.includes('Advanced Security is not enabled')
    if (notEnabled || isStatus(error, 401, 403, 404)) {
      log.debug('security alerts unavailable', { repo: scope.repo.name, status: Number((error as { status: number }).status) })
      return { dependabot: null, codeScanning: null }
    }
    throw error
  }
}

/** Branch policies, the last commit, and which well-known files exist. Each part degrades to null when unreadable. */
async function fetchFacts(client: AzureDevopsClient, scope: Scope, log: Logger): Promise<RepoFacts> {
  const { branch } = scope
  if (!branch) return toFacts(null, null, null)
  const version = { 'versionDescriptor.version': branch, 'versionDescriptor.versionType': 'branch' }
  const [policies, root, commits] = await Promise.all([
    tolerate('policies', scope, log, () => client.list<AdoPolicy>(`${scope.project}/_apis/git/policy/configurations`, { repositoryId: scope.repo.id, refName: `refs/heads/${branch}` })),
    tolerate('root', scope, log, () => client.list<{ path: string }>(`${scope.git}/items`, { scopePath: '/', recursionLevel: 'OneLevel', ...version })),
    tolerate('commits', scope, log, () => client.get<{ value: AdoCommit[] }>(`${scope.git}/commits`, { 'searchCriteria.itemVersion.version': branch, 'searchCriteria.$top': 1 })),
  ])
  return toFacts(policies, root?.map((item) => item.path) ?? null, commits?.value[0] ?? null)
}

async function fetchPipelines(client: AzureDevopsClient, scope: Scope, log: Logger): Promise<Pipeline[]> {
  const listed = await tolerate('pipelines', scope, log, () =>
    client.list<AdoDefinition>(`${scope.project}/_apis/build/definitions`, { repositoryId: scope.repo.id, repositoryType: 'TfsGit', includeAllProperties: true }),
  )
  const definitions = (listed ?? []).filter((d) => d.quality !== 'draft')
  if (definitions.length === 0) return []

  // One page of recent builds covers most pipelines. The gaps are filled per pipeline below.
  const recent = await fetchBuilds(client, scope, { $top: 100 })
  const defaultRef = `refs/heads/${scope.branch ?? 'main'}`

  return Promise.all(
    definitions.map(async (definition) => {
      const schedules = await fetchSchedules(client, scope, definition, log)
      const builds = new Map<number, AdoBuild>(recent.filter((b) => b.definition.id === definition.id).map((b) => [b.id, b]))
      const onDefault = (b: AdoBuild) => b.sourceBranch === defaultRef && b.reason !== 'pullRequest'
      const decisive = (b: AdoBuild) => b.status === 'completed' && b.result !== 'canceled' && b.result !== 'none'

      if (![...builds.values()].some((b) => onDefault(b) && decisive(b))) {
        for (const b of await fetchBuilds(client, scope, { definitions: definition.id, branchName: defaultRef, $top: 10 })) builds.set(b.id, b)
      }
      if (schedules.length > 0 && ![...builds.values()].some((b) => b.reason === 'schedule')) {
        for (const b of await fetchBuilds(client, scope, { definitions: definition.id, reasonFilter: 'schedule', $top: 1 })) builds.set(b.id, b)
      }

      const ordered: Run[] = [...builds.values()]
        .sort((a, b) => b.queueTime.localeCompare(a.queueTime))
        .slice(0, RUNS_PER_PIPELINE)
        .map(toRun)
      return toPipeline(definition, scope.project, schedules, ordered)
    }),
  )
}

/** Schedules set in the pipeline editor replace the ones in the YAML file, so the file is read only without them. */
async function fetchSchedules(client: AzureDevopsClient, scope: Scope, definition: AdoDefinition, log: Logger): Promise<Schedule[]> {
  const found = definitionSchedules(definition) ?? (await fetchFileSchedules(client, scope, definition))
  if (found.conditional > 0) {
    log.info('schedule fires only after a change; not watched for missed runs', { repo: scope.repo.name, pipeline: definition.name, schedules: found.conditional })
  }
  if (found.schedules.length > 0) log.debug('schedule watched for missed runs', { repo: scope.repo.name, pipeline: definition.name, schedules: found.schedules.length })
  return found.schedules
}

async function fetchFileSchedules(client: AzureDevopsClient, scope: Scope, definition: AdoDefinition) {
  const path = definition.process?.yamlFilename
  if (!path || !scope.branch) return { schedules: [], conditional: 0 }
  try {
    const file = await client.get<{ content?: string }>(`${scope.git}/items`, {
      path: `/${path.replace(/^\//, '')}`,
      includeContent: true,
      $format: 'json',
      'versionDescriptor.version': scope.branch,
      'versionDescriptor.versionType': 'branch',
    })
    return parseSchedules(file.content ?? '')
  } catch (error) {
    if (isStatus(error, 404)) return { schedules: [], conditional: 0 }
    throw error
  }
}
