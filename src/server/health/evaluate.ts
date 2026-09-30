import { CronExpressionParser } from 'cron-parser'
import type { Pipeline, RepoSnapshot, Run, RunConclusion } from '../../shared/model.ts'
import type { RepoHealth, RepoSettings, RuleKey, RuleSeverities, Signal } from '../../shared/rules.ts'

/** GitHub and its peers routinely start scheduled runs late; a run this late is not yet missed. */
const SCHEDULE_GRACE_MS = 90 * 60 * 1000

const FAILED: ReadonlySet<RunConclusion> = new Set(['failure', 'timed_out', 'startup_failure'])
/** Conclusions that say nothing about health, so a streak reads straight through them. */
const INCONCLUSIVE: ReadonlySet<RunConclusion> = new Set(['cancelled', 'skipped', 'neutral', 'stale', 'action_required'])
const BRANCH_TRIGGERS: ReadonlySet<Run['trigger']> = new Set(['push', 'schedule', 'manual', 'other'])

export interface EvaluateInput {
  repo: RepoSnapshot
  rules: RuleSeverities
  settings: RepoSettings
  now: Date
}

export function evaluate({ repo, rules, settings, now }: EvaluateInput): RepoHealth {
  const pipelines = repo.pipelines.filter((p) => p.state !== 'disabled' && !settings.mutedPipelines.includes(p.id))
  const branchRuns = pipelines.map((p) => ({
    pipeline: p,
    runs: p.runs.filter((r) => r.ref === repo.defaultBranch && BRANCH_TRIGGERS.has(r.trigger)),
  }))

  const signals: Signal[] = []
  const emit = (rule: RuleKey, signal: Omit<Signal, 'rule' | 'severity'>) => {
    const severity = settings.ruleOverrides[rule] ?? rules[rule]
    if (severity !== 'off') signals.push({ rule, severity, ...signal })
  }

  for (const { pipeline, runs } of branchRuns) {
    const failure = failingStreak(runs)
    if (!failure) continue
    emit('default_branch_failing', {
      title: `${pipeline.name} failing on ${repo.defaultBranch}`,
      detail: failure.count === 1 ? `Run #${failure.latest.number} failed` : `Last ${failure.count} runs failed`,
      since: failure.since,
      sinceIsLowerBound: failure.reachedEnd,
      url: failure.latest.url,
      pipelineId: pipeline.id,
    })
  }

  for (const pipeline of pipelines) {
    const stale = scheduleStale(pipeline, now)
    if (stale) emit('schedule_stale', { ...stale, sinceIsLowerBound: false, pipelineId: pipeline.id })
  }

  const release = releaseFailure(pipelines)
  if (release) emit('release_build_failed', release)

  const { dependabot, codeScanning } = repo.security
  if (dependabot && dependabot.critical > 0) {
    emit('dependabot_critical', {
      title: plural(dependabot.critical, 'critical dependency alert'),
      detail: dependabot.high > 0 ? `Plus ${dependabot.high} high` : 'Open in Dependabot',
      since: null,
      sinceIsLowerBound: false,
      url: dependabot.url,
      pipelineId: null,
    })
  }
  if (codeScanning && codeScanning.critical > 0) {
    emit('code_scanning_critical', {
      title: plural(codeScanning.critical, 'critical code scanning alert'),
      detail: codeScanning.high > 0 ? `Plus ${codeScanning.high} high` : 'Open in code scanning',
      since: null,
      sinceIsLowerBound: false,
      url: codeScanning.url,
      pipelineId: null,
    })
  }

  const failingPrs = repo.pullRequests.filter((pr) => !pr.draft && pr.checks === 'failure')
  const [firstPr] = failingPrs
  if (firstPr) {
    emit('pr_checks_failing', {
      title: plural(failingPrs.length, 'pull request') + ' failing checks',
      detail: failingPrs.map((pr) => `#${pr.number} ${pr.title}`).join(' · '),
      since: null,
      sinceIsLowerBound: false,
      url: firstPr.url,
      pipelineId: null,
    })
  }

  const decisive = branchRuns.flatMap(({ runs }) => runs.filter(isDecisive))
  const allRuns = pipelines.flatMap((p) => p.runs)
  const health = {
    signals,
    running: allRuns.some((r) => r.status !== 'completed'),
    successRate: decisive.length ? decisive.filter((r) => r.conclusion === 'success').length / decisive.length : null,
    lastRunAt: allRuns.reduce<string | null>((max, r) => (max === null || r.createdAt > max ? r.createdAt : max), null),
  }

  if (settings.muted) return { ...health, level: 'quiet', quietReason: 'muted' }
  if (repo.archived) return { ...health, level: 'quiet', quietReason: 'archived' }
  if (signals.some((s) => s.severity === 'red')) return { ...health, level: 'red', quietReason: null }
  if (signals.some((s) => s.severity === 'amber')) return { ...health, level: 'amber', quietReason: null }
  if (pipelines.length === 0) return { ...health, level: 'quiet', quietReason: 'no_pipelines' }
  return { ...health, level: 'green', quietReason: null }
}

function isDecisive(run: Run): run is Run & { conclusion: RunConclusion } {
  return run.status === 'completed' && run.conclusion !== null && !INCONCLUSIVE.has(run.conclusion)
}

/** The unbroken run of failures at the head of `runs` (newest first), or null when the latest decisive run passed. */
function failingStreak(runs: Run[]) {
  const decisive = runs.filter(isDecisive)
  const [latest] = decisive
  if (!latest || !FAILED.has(latest.conclusion)) return null
  const successAt = decisive.findIndex((r) => !FAILED.has(r.conclusion))
  const streak = successAt === -1 ? decisive : decisive.slice(0, successAt)
  const oldest = streak[streak.length - 1] ?? latest
  return { latest, count: streak.length, since: oldest.createdAt, reachedEnd: successAt === -1 }
}

function scheduleStale(pipeline: Pipeline, now: Date): Omit<Signal, 'rule' | 'severity' | 'sinceIsLowerBound' | 'pipelineId'> | null {
  if (pipeline.schedules.length === 0) return null
  if (pipeline.state === 'dormant') {
    return {
      title: `${pipeline.name} schedule switched off`,
      detail: 'The forge disabled it after a long stretch without repository activity',
      since: pipeline.updatedAt,
      url: pipeline.url,
    }
  }

  const lastScheduled = pipeline.runs.find((r) => r.trigger === 'schedule')
  // With no scheduled run on record, the clock starts when the workflow last changed.
  const baseline = new Date(lastScheduled?.createdAt ?? pipeline.updatedAt)
  const cutoff = now.getTime() - SCHEDULE_GRACE_MS
  const missed = pipeline.schedules
    .flatMap((expr) => nextFires(expr, baseline, 2))
    .filter((t) => t.getTime() <= cutoff)
    .sort((a, b) => a.getTime() - b.getTime())

  const [firstMissed] = missed
  if (missed.length < 2 || !firstMissed) return null
  return {
    title: `${pipeline.name} schedule stopped`,
    detail: lastScheduled ? `No scheduled run since ${lastScheduled.createdAt.slice(0, 10)}` : 'Never ran on its schedule',
    since: firstMissed.toISOString(),
    url: pipeline.url,
  }
}

function nextFires(expression: string, after: Date, count: number): Date[] {
  try {
    const cron = CronExpressionParser.parse(expression, { currentDate: after, tz: 'UTC' })
    return Array.from({ length: count }, () => cron.next().toDate())
  } catch {
    return []
  }
}

/** A failed pipeline run for the newest release or tag that has runs. */
function releaseFailure(pipelines: Pipeline[]): Omit<Signal, 'rule' | 'severity'> | null {
  const releaseRuns = pipelines.flatMap((pipeline) =>
    pipeline.runs.filter((r) => r.trigger === 'release' || r.trigger === 'tag').map((run) => ({ pipeline, run })),
  )
  const newest = releaseRuns.reduce<(typeof releaseRuns)[number] | null>(
    (max, entry) => (max === null || entry.run.createdAt > max.run.createdAt ? entry : max),
    null,
  )
  if (!newest) return null

  const ref = newest.run.ref
  const failed = pipelines.flatMap((pipeline) => {
    const latest = pipeline.runs.filter((r) => (r.trigger === 'release' || r.trigger === 'tag') && r.ref === ref).find(isDecisive)
    return latest && FAILED.has(latest.conclusion) ? [{ pipeline, run: latest }] : []
  })
  const [first] = failed
  if (!first) return null
  return {
    title: ref ? `Release ${ref} build failed` : 'Release build failed',
    detail: failed.map((f) => f.pipeline.name).join(' · '),
    since: first.run.createdAt,
    sinceIsLowerBound: false,
    url: first.run.url,
    pipelineId: first.pipeline.id,
  }
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`
}
