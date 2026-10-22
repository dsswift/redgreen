// Pushes every repository to an Orrery hub as a `repository` entity, so the
// catalog there lists what this board watches, every service found in one as
// a `service` entity, and every finished deployment the forge recorded as a
// `deployment` entity. The blueprints travel with the push and are offered to
// the hub first. Repositories and services each go as one snapshot run per
// account, and a single repo and its services are patched as soon as it
// syncs. Deployments only accumulate, so they go in an incremental run of
// their own.

import { LANGUAGES, listedLanguage, type Language } from '../shared/languages.ts'
import type { RepoSnapshot } from '../shared/model.ts'
import type { Level, RepoHealth } from '../shared/rules.ts'
import type { Config } from './config.ts'
import type { Estate } from './estate.ts'
import type { Events } from './events.ts'
import type { Logger } from './log.ts'
import type { AccountRow, Store } from './store.ts'

export interface RepositoryEntity {
  blueprint: 'repository'
  key: string
  title: string
  fields: Record<string, unknown>
}

export interface ServiceEntity {
  blueprint: 'service'
  key: string
  title: string
  icon?: string
  fields: Record<string, unknown>
  relations: { repository: string }
}

export interface DeploymentEntity {
  blueprint: 'deployment'
  key: string
  title: string
  fields: Record<string, unknown>
  relations: { repository: string }
}

const LEVELS = ['red', 'amber', 'green', 'grey']
const SUCCEEDED = { field: 'status', op: 'eq', value: 'succeeded' }
const FAILED = { field: 'status', op: 'eq', value: 'failed' }

/** What `toRepository` writes. The hub creates it when it has none and leaves an existing one as it is. */
export const REPOSITORY_BLUEPRINT = {
  id: 'repository',
  title: 'Repository',
  group: 'Catalog',
  description:
    'A Git repository on a forge, pushed by the repo health board: language, branch protection, last push, pipeline health, alert counts. Relate your own blueprints to it: the service built from it, the team that owns it.',
  icon: 'repository',
  status: { field: 'health' },
  fields: {
    description: { type: 'string', description: 'The repository description.' },
    url: { type: 'url', title: 'Repository' },
    provider: { type: 'string', chip: true, description: 'The forge the repository lives on, e.g. github, gitlab, or azuredevops.' },
    organization: { type: 'string', chip: true, description: 'The account or group the repository lives under.' },
    name: { type: 'string', description: 'The repository name without its organization.' },
    language: { type: 'enum', values: [...LANGUAGES], sticky: true, description: 'The main language of the repository: the one with the largest share of its code. It is a best guess, so a person who knows better changes it, and that change stays.' },
    visibility: { type: 'enum', values: ['public', 'private', 'internal'] },
    archived: { type: 'boolean' },
    fork: { type: 'boolean' },
    topics: { type: 'array' },
    default_branch: { type: 'string' },
    branch_protected: { type: 'boolean', title: 'Is branch protected' },
    requires_code_review: { type: 'boolean', title: 'Requires code review' },
    required_approvals: { type: 'number' },
    requires_code_owner_review: { type: 'boolean', title: 'Require code owner review' },
    has_codeowners: { type: 'boolean', title: 'Has CODEOWNERS' },
    has_readme: { type: 'boolean', title: 'Has README' },
    has_dockerfile: { type: 'boolean', title: 'Has Dockerfile' },
    has_ci: { type: 'boolean', title: 'Has CI' },
    last_push: { type: 'datetime' },
    freshness_days: { type: 'number', title: 'Freshness (days)', calculation: 'days_since(last_push)' },
    last_committer: { type: 'string' },
    last_commit_at: { type: 'datetime' },
    open_prs: { type: 'number', title: 'Open PRs' },
    stale_prs: { type: 'number', title: 'Stale PRs (7d+)' },
    open_issues: { type: 'number' },
    stars: { type: 'number' },
    pipelines: { type: 'number' },
    pipelines_failing: { type: 'number' },
    workflow_failure_rate: { type: 'number', title: 'Workflow failure rate (%)' },
    last_release: { type: 'string' },
    last_release_at: { type: 'datetime' },
    dependabot_critical: { type: 'number', title: 'Dependabot critical' },
    dependabot_high: { type: 'number', title: 'Dependabot high' },
    code_scanning_critical: { type: 'number', title: 'Code scanning critical' },
    code_scanning_high: { type: 'number', title: 'Code scanning high' },
    open_alerts: { type: 'number', title: 'Total open alerts', calculation: 'coalesce(dependabot_critical, 0) + coalesce(dependabot_high, 0) + coalesce(code_scanning_critical, 0) + coalesce(code_scanning_high, 0)' },
    health: { type: 'enum', values: LEVELS },
    health_reason: { type: 'string' },
    board: { type: 'url', title: 'Repo health board' },
    notes: { type: 'markdown' },
  },
  relations: {},
}

/** What a repository can say once the hub holds deployments that point at it. Offered after the deployment blueprint is accepted, because these read through it. */
const REPOSITORY_DEPLOYMENT_FIELDS = {
  id: 'repository',
  title: 'Repository',
  fields: {
    last_deployed_at: { type: 'datetime', title: 'Last deployed', aggregation: { relation: '<-deployment:repository', fn: 'max', field: 'deployed_at', where: SUCCEEDED } },
    deployments_30d: { type: 'number', title: 'Deployments (30d)', aggregation: { relation: '<-deployment:repository', fn: 'count', where: SUCCEEDED, window: { field: 'deployed_at', last: '30d' } } },
    failed_deployments_30d: { type: 'number', title: 'Failed deployments (30d)', aggregation: { relation: '<-deployment:repository', fn: 'count', where: FAILED, window: { field: 'deployed_at', last: '30d' } } },
    deployments_per_week: { type: 'number', title: 'Deployments per week', description: 'Successful deployments per week over the last 90 days.', aggregation: { relation: '<-deployment:repository', fn: 'count', where: SUCCEEDED, window: { field: 'deployed_at', last: '90d' }, per: 'week' } },
    change_failure_rate: { type: 'number', title: 'Change failure rate (%)', description: "The share of the last 30 days' deployments that failed.", calculation: 'deployments_30d + failed_deployments_30d > 0 ? round(failed_deployments_30d / (deployments_30d + failed_deployments_30d) * 100) : null' },
  },
}

/** What `toServices` writes. A hub that already has a service blueprint gains the fields and the relation it lacks, and keeps the rest of it as people made it. */
export const SERVICE_BLUEPRINT = {
  id: 'service',
  title: 'Service',
  group: 'Catalog',
  description:
    'One piece of software people run and answer for: an app, an API, a job, a command-line tool, a library others build on, or the infrastructure it stands on. The repo health board finds each one from the files that build it and pushes where it lives, what kind it is, and what it is written in. What people decide about it, such as how much it matters, its lifecycle, and its runbook, is theirs to add here.',
  icon: 'service',
  fields: {
    path: { type: 'string', description: 'Where the service sits inside its repository, empty for one that is the whole repository.' },
    last_changed_at: { type: 'datetime', title: 'Last changed', description: "When the newest commit on the default branch touched the service's folder. A repository that holds several services moves each one only when its own folder changes." },
    last_changed_by: { type: 'string', title: 'Last changed by', description: "Who made the newest commit on the default branch that touched the service's folder, as the forge names them. A bot or release tool counts." },
    last_human_changed_at: { type: 'datetime', title: 'Last human change', description: "When the newest commit by a person, not a bot or release tool, touched the service's folder. Empty when none of its recent commits were by a person." },
    last_human_changed_by: { type: 'string', title: 'Last human change by', description: 'Who made that commit, as the forge names them.' },
    type: { type: 'enum', values: ['backend', 'frontend', 'library', 'infrastructure', 'tool', 'docs', 'other'], description: 'What kind of thing it is, read from the files that build it.' },
    language: { type: 'enum', values: [...LANGUAGES], sticky: true, description: 'What the service is written in, read from the files that build it. A repository that holds several services can hold several languages; this is the one for this service. A person who knows better changes it, and that change stays.' },
    has_readme: { type: 'boolean', title: 'Has README', description: "A README in the service's own folder, or in the nearest folder above it that holds no other service. A monorepo's root README documents no single service." },
    has_dockerfile: { type: 'boolean', title: 'Has Dockerfile', description: 'A Dockerfile that builds this service, found the same way as its README. Two services in one repository can differ.' },
    has_tests: { type: 'boolean', title: 'Has tests', description: 'Automated tests that belong to this service: test files in its folder, or a test project beside it named for it, such as Tests.Orders.Api.' },
  },
  relations: {
    repository: { target: 'repository', many: false, title: 'Homed in', inverseTitle: 'Services' },
  },
}

/** The picture a service wears for the language it is written in. A language with none in the hub's set leaves the blueprint's. */
const LANGUAGE_ICONS: Partial<Record<Language, string>> = {
  'C#': 'csharp',
  Go: 'go',
  Python: 'python',
  TypeScript: 'typescript',
  JavaScript: 'javascript',
  Terraform: 'terraform',
  Bicep: 'azure',
  PowerShell: 'powershell',
  Shell: 'bash',
  Swift: 'swift',
  Rust: 'rust',
  'C++': 'cpp',
  Lua: 'lua',
  HTML: 'html',
  CSS: 'css',
  SQL: 'database',
}

/** What `toDeployments` writes. A hub that already has a deployment blueprint from another pusher gains the fields and the relation it lacks. */
export const DEPLOYMENT_BLUEPRINT = {
  id: 'deployment',
  title: 'Deployment',
  group: 'Catalog',
  description:
    'One time a version was rolled out. These come from the deployments a forge records when a pipeline job names an environment. Deployments only accumulate, so they leave by age. Count them over a window to see how often and how reliably something deploys.',
  icon: 'rocket',
  canvas: { default: 'hidden' },
  lifecycle: { expire: { after: '180d', field: 'deployed_at' } },
  fields: {
    status: { type: 'enum', values: ['succeeded', 'failed'] },
    environment_name: { type: 'string', chip: true, title: 'Environment', description: 'The environment as the forge names it.' },
    revision: { type: 'string', description: 'The commit that was deployed.' },
    ref: { type: 'string', description: 'The branch or tag it was deployed from.' },
    deployed_at: { type: 'datetime', description: 'When it finished.' },
    started_at: { type: 'datetime' },
    duration_seconds: { type: 'number', title: 'Duration (s)' },
    initiated_by: { type: 'string', description: 'Who started it.' },
    url: { type: 'url', title: 'Open' },
  },
  relations: {
    repository: { target: 'repository', many: false, title: 'Repository', inverseTitle: 'Deployments' },
  },
}

const repositoryKey = (repo: Pick<RepoSnapshot, 'provider' | 'fullName'>) => `${repo.provider}/${repo.fullName}`.toLowerCase()

/** Every service found in a repo, pointing at its repository. Empty when the services could not be read. Pure, so it is tested without a hub. */
export function toServices(repo: RepoSnapshot): ServiceEntity[] {
  const key = repositoryKey(repo)
  return (repo.services?.services ?? []).map((s) => {
    const icon = s.language ? LANGUAGE_ICONS[s.language] : undefined
    return {
      blueprint: 'service',
      key: s.id ? `${key}/${s.id.toLowerCase()}` : key,
      title: s.name,
      ...(icon ? { icon } : {}),
      fields: { path: s.path, type: s.type, language: s.language, last_changed_at: s.changedAt ?? null, last_changed_by: s.changedBy ?? null, last_human_changed_at: s.humanChangedAt ?? null, last_human_changed_by: s.humanChangedBy ?? null, has_readme: s.files?.readme ?? null, has_dockerfile: s.files?.dockerfile ?? null, has_tests: s.files?.tests ?? null },
      relations: { repository: key },
    }
  })
}

/** Every deployment of a repo that reached an end. One still running is left for the sweep after it finishes. Pure, so it is tested without a hub. */
export function toDeployments(repo: RepoSnapshot): DeploymentEntity[] {
  const key = repositoryKey(repo)
  return (repo.deployments ?? [])
    .filter((d) => (d.status === 'succeeded' || d.status === 'failed') && d.finishedAt !== null)
    .map((d) => {
      const seconds = (Date.parse(d.finishedAt!) - Date.parse(d.createdAt)) / 1000
      const fields: Record<string, unknown> = { status: d.status, environment_name: d.environment, revision: d.sha, ref: d.ref, deployed_at: d.finishedAt, started_at: d.createdAt, initiated_by: d.actor }
      if (Number.isFinite(seconds) && seconds >= 0) fields.duration_seconds = Math.round(seconds)
      if (d.url) fields.url = d.url
      return { blueprint: 'deployment', key: `${key}/${d.id}`, title: `${repo.name} to ${d.environment}`, fields, relations: { repository: key } }
    })
}

const LEVEL: Record<Level, string> = { red: 'red', amber: 'amber', green: 'green', quiet: 'grey', unknown: 'grey' }
const STALE_PR_MS = 7 * 86_400_000

/** Shapes one repo. Pure, so it is tested without a hub. */
export function toRepository(repo: RepoSnapshot, account: Pick<AccountRow, 'login'>, health: RepoHealth | null, boardUrl: string, now = Date.now()): RepositoryEntity {
  const decisive = repo.pipelines.flatMap((p) => p.runs).filter((r) => r.status === 'completed' && r.conclusion !== 'cancelled' && r.conclusion !== 'skipped' && r.ref === repo.defaultBranch)
  const failed = decisive.filter((r) => r.conclusion !== 'success' && r.conclusion !== 'neutral').length
  const failing = repo.pipelines.filter((p) => {
    const last = p.runs.find((r) => r.status === 'completed' && r.ref === repo.defaultBranch && r.conclusion !== 'cancelled' && r.conclusion !== 'skipped')
    return !!last && last.conclusion !== 'success' && last.conclusion !== 'neutral'
  }).length
  const release = repo.releases[0]
  const worst = health?.signals.find((s) => s.severity === health.level) ?? health?.signals[0]
  const fields: Record<string, unknown> = {
    description: repo.description,
    url: repo.url,
    provider: repo.provider,
    organization: account.login,
    name: repo.name,
    language: listedLanguage(repo.language),
    visibility: repo.visibility,
    archived: repo.archived,
    fork: repo.fork,
    default_branch: repo.defaultBranch,
    last_push: repo.pushedAt,
    open_prs: repo.openPullRequests,
    stale_prs: repo.pullRequests.filter((p) => now - Date.parse(p.updatedAt) > STALE_PR_MS).length,
    open_issues: repo.openIssues,
    stars: repo.stars,
    pipelines: repo.pipelines.length,
    pipelines_failing: failing,
    workflow_failure_rate: decisive.length ? Math.round((failed / decisive.length) * 100) : null,
    has_ci: repo.pipelines.length > 0,
    last_release: release?.tag ?? null,
    last_release_at: release?.publishedAt ?? null,
    dependabot_critical: repo.security.dependabot?.critical ?? null,
    dependabot_high: repo.security.dependabot?.high ?? null,
    code_scanning_critical: repo.security.codeScanning?.critical ?? null,
    code_scanning_high: repo.security.codeScanning?.high ?? null,
    health: LEVEL[health?.level ?? 'unknown'],
    health_reason: health?.level === 'quiet' ? (health.quietReason ?? 'quiet') : (worst?.title ?? null),
    board: boardUrl,
  }
  if (repo.facts) {
    Object.assign(fields, {
      topics: repo.facts.topics,
      branch_protected: repo.facts.branchProtected,
      requires_code_review: repo.facts.requiresReview,
      required_approvals: repo.facts.requiredApprovals,
      requires_code_owner_review: repo.facts.requiresCodeOwnerReview,
      has_codeowners: repo.facts.hasCodeowners,
      has_readme: repo.facts.hasReadme,
      has_dockerfile: repo.facts.hasDockerfile,
      last_committer: repo.facts.lastCommitter,
      last_commit_at: repo.facts.lastCommitAt,
    })
  }
  return { blueprint: 'repository', key: repositoryKey(repo), title: repo.name, fields }
}

export class OrreryPusher {
  private readonly url: string
  private readonly token: string
  private readonly config: Config
  private readonly store: Store
  private readonly estate: Estate
  private readonly events: Events
  private readonly log: Logger
  private offered = false
  /** False when the hub refused the deployment blueprint: the source is not registered for it. */
  private deployments = false
  /** False when the hub refused the service blueprint: the source is not registered for it. */
  private services = false
  private timer: NodeJS.Timeout | null = null
  private unsubscribe: (() => void) | null = null

  constructor(config: Config, store: Store, estate: Estate, events: Events, log: Logger) {
    this.url = (config.ORRERY_URL ?? '').replace(/\/$/, '')
    this.token = config.ORRERY_TOKEN ?? ''
    this.config = config
    this.store = store
    this.estate = estate
    this.events = events
    this.log = log
  }

  enabled(): boolean {
    return !!this.url && !!this.token
  }

  start(): void {
    if (!this.enabled()) {
      this.log.info('orrery push off: ORRERY_URL and ORRERY_TOKEN are not both set')
      return
    }
    this.log.info('orrery push on', { url: this.url })
    const interval = this.config.SYNC_INTERVAL_MINUTES * 60_000
    // The first sweep needs a head start, so the first snapshot is not half empty.
    setTimeout(() => void this.pushAll('startup'), 90_000).unref()
    this.timer = setInterval(() => void this.pushAll('interval'), interval)
    this.unsubscribe = this.events.subscribe((event) => {
      if (event.type === 'repo') void this.pushRepo(event.id)
    })
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer)
    this.unsubscribe?.()
  }

  private boardUrl(repoId: string): string {
    return `${this.config.PUBLIC_URL.replace(/\/$/, '')}/repos/${encodeURIComponent(repoId)}`
  }

  private async call(method: string, path: string, body?: unknown): Promise<unknown> {
    const res = await fetch(this.url + path, { method, headers: { authorization: `Bearer ${this.token}`, 'content-type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(60_000) })
    const text = await res.text()
    if (!res.ok && res.status !== 207) throw new Error(`orrery ${method} ${path}: ${res.status} ${text.slice(0, 300)}`)
    return text ? JSON.parse(text) : null
  }

  /** Brings the blueprint once per start. A hub that refuses it would refuse every entity too, so the push waits for the next sweep. */
  private async offer(): Promise<boolean> {
    if (this.offered) return true
    try {
      const res = (await this.call('PUT', '/ingest/blueprints', { blueprints: [REPOSITORY_BLUEPRINT, SERVICE_BLUEPRINT, DEPLOYMENT_BLUEPRINT] })) as { created?: string[]; extended?: string[]; rejected?: { blueprint: string; reason: string }[] }
      // Repositories are the point; a hub that will not take deployments still gets them.
      const refused = res.rejected?.find((r) => r.blueprint === 'repository')
      if (refused) {
        this.log.error('orrery refused the repository blueprint', { reason: refused.reason })
        return false
      }
      const services = res.rejected?.find((r) => r.blueprint === 'service')
      this.services = !services
      if (services) this.log.warn('orrery refused the service blueprint; services are not pushed', { reason: services.reason })
      const deployments = res.rejected?.find((r) => r.blueprint === 'deployment')
      this.deployments = !deployments
      if (deployments) this.log.warn('orrery refused the deployment blueprint; deployments are not pushed', { reason: deployments.reason })
      else {
        const counted = (await this.call('PUT', '/ingest/blueprints', { blueprints: [REPOSITORY_DEPLOYMENT_FIELDS] })) as { rejected?: { reason: string }[] }
        if (counted.rejected?.length) this.log.warn('orrery refused the deployment counts on repository; deployments are pushed without them', { reason: counted.rejected[0]?.reason })
      }
      this.offered = true
      this.log.info('orrery blueprints offered', { created: res.created ?? [], extended: res.extended ?? [], services: this.services, deployments: this.deployments })
      return true
    } catch (error) {
      this.log.error('orrery blueprint offer failed', { error })
      return false
    }
  }

  /** One repo, right after it synced. Merges fields; never deletes. */
  async pushRepo(repoId: string): Promise<void> {
    const row = this.store.getRepo(repoId)
    const account = row && this.store.getAccount(row.accountId)
    if (!row?.snapshot || !account || account.status !== 'active') return
    if (!(await this.offer())) return
    const entity = toRepository(row.snapshot, account, this.estate.healthOf(row), this.boardUrl(row.id))
    try {
      const scope = `${account.provider}/${account.login}`.toLowerCase()
      await this.call('PATCH', `/ingest/entities/repository/${encodeURIComponent(entity.key)}`, { title: entity.title, fields: entity.fields, scope })
      const services = this.services ? toServices(row.snapshot) : []
      for (const s of services) await this.call('PATCH', `/ingest/entities/service/${encodeURIComponent(s.key)}`, { title: s.title, ...(s.icon ? { icon: s.icon } : {}), fields: s.fields, relations: s.relations, scope: `${scope}/services` })
      this.log.debug('orrery repo pushed', { repo: row.fullName, services: services.length })
    } catch (error) {
      this.log.warn('orrery repo push failed', { repo: row.fullName, error })
    }
  }

  /** Every active account as one run. A snapshot when every repo has synced; incremental otherwise, so nothing half read is deleted. */
  async pushAll(reason: string): Promise<void> {
    if (!(await this.offer())) return
    const rows = this.store.listRepos()
    for (const account of this.store.listAccounts()) {
      if (account.status !== 'active') continue
      const mine = rows.filter((r) => r.accountId === account.id)
      if (mine.length === 0) continue
      const synced = mine.filter((r) => r.snapshot)
      const mode = synced.length === mine.length ? 'snapshot' : 'incremental'
      const scope = `${account.provider}/${account.login}`.toLowerCase()
      try {
        const entities = synced.map((r) => toRepository(r.snapshot!, account, this.estate.healthOf(r), this.boardUrl(r.id)))
        const rejected = await this.run(mode, scope, entities, 'repository')
        this.log.info('orrery push done', { reason, account: account.login, mode, repositories: entities.length, unsynced: mine.length - synced.length, rejected })
        await this.pushServices(account.login, scope, mode, synced.map((r) => r.snapshot!))
        await this.pushDeployments(account.login, scope, synced.flatMap((r) => toDeployments(r.snapshot!)))
      } catch (error) {
        this.log.error('orrery push failed', { reason, account: account.login, error })
      }
    }
  }

  /** One run of entities in batches, committed. Answers how many the hub rejected. */
  private async run(mode: 'snapshot' | 'incremental', scope: string, entities: (RepositoryEntity | ServiceEntity | DeploymentEntity)[], kind: string): Promise<number> {
    const run = (await this.call('POST', '/ingest/runs', { mode, scope })) as { id: string }
    let rejected = 0
    for (let i = 0; i < entities.length; i += 100) {
      const res = (await this.call('PUT', `/ingest/runs/${encodeURIComponent(run.id)}/entities`, { entities: entities.slice(i, i + 100) })) as { rejected?: { key?: string; field?: string; reason: string }[] }
      for (const r of res.rejected ?? []) {
        rejected++
        this.log.warn(`orrery rejected a ${kind}`, { key: r.key, field: r.field, reason: r.reason })
      }
    }
    await this.call('POST', `/ingest/runs/${encodeURIComponent(run.id)}/commit`)
    return rejected
  }

  /** A snapshot only when every repo's services were read, so a service is never dropped because its repo could not be. */
  private async pushServices(account: string, scope: string, repoMode: 'snapshot' | 'incremental', snapshots: RepoSnapshot[]): Promise<void> {
    if (!this.services) {
      this.log.debug('no services pushed; the hub refused the blueprint', { account })
      return
    }
    const unread = snapshots.filter((s) => !s.services).length
    const mode = repoMode === 'snapshot' && unread === 0 ? 'snapshot' : 'incremental'
    if (mode === 'incremental') this.log.info('services pushed without deleting; some repos are unread', { account, unread })
    const entities = snapshots.flatMap(toServices)
    const rejected = await this.run(mode, `${scope}/services`, entities, 'service')
    this.log.info('orrery services pushed', { account, mode, services: entities.length, rejected })
  }

  /** Deployments never leave a forge's list in order, so the run is incremental: the hub keeps each one until it expires. */
  private async pushDeployments(account: string, scope: string, entities: DeploymentEntity[]): Promise<void> {
    if (!this.deployments || entities.length === 0) {
      this.log.debug('no deployments pushed', { account, accepted: this.deployments, found: entities.length })
      return
    }
    const rejected = await this.run('incremental', `${scope}/deployments`, entities, 'deployment')
    this.log.info('orrery deployments pushed', { account, deployments: entities.length, rejected })
  }
}
