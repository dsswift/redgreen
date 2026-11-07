// Proves redgreen's blueprints with Orrery's own code: every blueprint passes
// the hub's schema, every calculation compiles, and the health rules and DORA
// fields give the expected answers on sample entities, with aggregations run
// through the hub's aggregate() and calculations in field order, as the hub
// computes them.
//
//   go run ./scripts/blueprints > /tmp/blueprints.json
//   node scripts/check-expressions.mts /tmp/blueprints.json [orrery checkout]
//
// The Orrery checkout defaults to ../orrery beside this repository.

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [file, orreryDir = resolve(import.meta.dirname, '../../orrery')] = process.argv.slice(2)
if (!file) throw new Error('usage: node scripts/check-expressions.mts <blueprints.json> [orrery checkout]')
const from = (p: string) => import(pathToFileURL(resolve(orreryDir, p)).href)
const { compile } = await from('src/server/expr.ts')
const { blueprintSchema } = await from('src/server/blueprints.ts')
const { aggregate } = await from('src/server/read.ts')

type Def = { type: string; default?: unknown; calculation?: string; aggregation?: Record<string, unknown> }
type Bp = { id: string; fields: Record<string, Def> }
const blueprints: Bp[] = JSON.parse(readFileSync(file, 'utf8'))
const byId = new Map(blueprints.map((b) => [b.id, b]))

let failures = 0
const fail = (msg: string) => {
  failures++
  console.error(`FAIL ${msg}`)
}

// 1. The hub's schema takes every blueprint, and every calculation compiles.
let compiled = 0
for (const bp of blueprints) {
  const parsed = blueprintSchema.safeParse(bp)
  if (!parsed.success) fail(`${bp.id}: schema: ${parsed.error.message}`)
  for (const [name, def] of Object.entries(bp.fields)) {
    if (!def.calculation) continue
    try {
      compile(def.calculation)
      compiled++
    } catch (e) {
      fail(`${bp.id}.${name}: ${(e as Error).message}`)
    }
  }
}
console.log(`schema: ${blueprints.length} blueprints; compiled: ${compiled} calculations`)

// 2. Compute an entity the way the hub does: stored fields with defaults, then
// aggregations over related entities, then calculations in field order.
const DAY = 86_400_000
const ago = (days: number) => new Date(Date.now() - days * DAY).toISOString()
const entity = (fields: Record<string, unknown>) => ({ id: 'x:y', blueprint: 'x', key: 'y', title: 'y', state: 'active', fields, relations: {}, status: { level: 'green', reasons: [] } })

function compute(id: string, stored: Record<string, unknown>, related: ReturnType<typeof entity>[] = [], title = 'thing') {
  const bp = byId.get(id)!
  const fields: Record<string, unknown> = {}
  for (const [name, def] of Object.entries(bp.fields)) fields[name] = name in stored ? stored[name] : (def.default ?? null)
  for (const [name, def] of Object.entries(bp.fields)) if (def.aggregation) fields[name] = aggregate(def.aggregation, related)
  const scope = new Proxy(fields, { get: (f, n) => (n === 'title' ? title : f[n as string]), has: () => true })
  for (const [name, def] of Object.entries(bp.fields)) {
    if (!def.calculation) continue
    try {
      fields[name] = compile(def.calculation).evaluate(scope)
    } catch {
      fields[name] = null
    }
  }
  return fields
}

let cases = 0
function expect(label: string, got: Record<string, unknown>, want: Record<string, unknown>) {
  cases++
  for (const [k, v] of Object.entries(want)) {
    if (JSON.stringify(got[k]) !== JSON.stringify(v)) fail(`${label}: ${k} = ${JSON.stringify(got[k])}, want ${JSON.stringify(v)}`)
  }
}

// Pipelines.
const pipe = (f: Record<string, unknown>) => compute('sw_pipeline', { name: 'CI', default_branch: 'main', state: 'enabled', schedule_state: 'none', default_branch_failing: false, release_build_failed: false, flaky: false, ...f })
expect('pipeline green', pipe({}), { status: 'green', status_reason: null })
expect('pipeline failing', pipe({ default_branch_failing: true }), { status: 'red', status_reason: 'CI failing on main', default_branch_failing_level: 'red' })
expect('pipeline flaky', pipe({ flaky: true }), { status: 'amber', status_reason: 'CI is flaky' })
expect('pipeline failing and flaky', pipe({ default_branch_failing: true, flaky: true }), { status: 'red', status_reason: 'CI failing on main' })
expect('pipeline muted', pipe({ default_branch_failing: true, muted: true }), { status: 'grey', status_reason: 'muted' })
expect('pipeline disabled', pipe({ state: 'disabled', default_branch_failing: true }), { status: 'grey', status_reason: 'disabled' })
expect('pipeline schedule missed', pipe({ name: 'Nightly', schedule_state: 'missed' }), { schedule_stale: true, status: 'amber', status_reason: 'Nightly schedule stopped' })
expect('pipeline schedule switched off', pipe({ name: 'Nightly', state: 'dormant', schedule_state: 'disabled' }), { status: 'amber', status_reason: 'Nightly schedule switched off by the forge' })
expect('pipeline schedule on time', pipe({ schedule_state: 'on_time' }), { schedule_stale: false, status: 'green' })
expect('pipeline release failed', pipe({ release_build_failed: true, release_ref: 'v1.2.0' }), { status: 'red', status_reason: 'CI build failed for release v1.2.0' })
expect('pipeline failing set off', pipe({ default_branch_failing: true, default_branch_failing_severity: 'off', flaky: true }), { status: 'amber', status_reason: 'CI is flaky', default_branch_failing_level: null })
expect('pipeline failing set info', pipe({ default_branch_failing: true, default_branch_failing_severity: 'info' }), { status: 'green', default_branch_failing_level: 'info' })
expect('pipeline flaky set red', pipe({ flaky: true, flaky_severity: 'red' }), { status: 'red', status_reason: 'CI is flaky' })

// Repositories roll up their pipelines' statuses through aggregations.
const piped = (status: string, reason: string | null = null) => entity({ status, status_reason: reason })
const repo = (f: Record<string, unknown>, pipelines: ReturnType<typeof entity>[] = [piped('green')]) => compute('sw_repository', { last_push: ago(1), archived: false, ...f }, pipelines)
expect('repo no pipelines', repo({}, []), { status: 'grey', status_reason: 'no pipelines', pipelines_active: 0 })
expect('repo only grey pipelines', repo({}, [piped('grey', 'muted')]), { status: 'grey', status_reason: 'no pipelines' })
expect('repo green', repo({}), { status: 'green', status_reason: null, pipelines_active: 1 })
expect('repo green with a muted pipeline', repo({}, [piped('green'), piped('grey', 'muted')]), { status: 'green', pipelines_active: 1 })
expect('repo red pipeline', repo({}, [piped('green'), piped('red', 'CI failing on main')]), { status: 'red', status_reason: 'CI failing on main', pipelines_red: 1 })
expect('repo amber pipeline', repo({}, [piped('amber', 'CI is flaky')]), { status: 'amber', status_reason: 'CI is flaky' })
expect('repo critical alerts', repo({ dependabot_critical: 1, code_scanning_critical: 1 }), { critical_alerts: true, status: 'red', status_reason: '2 critical alerts open', open_alerts: 2 })
expect('repo alerts set off', repo({ dependabot_critical: 1, critical_alerts_severity: 'off' }), { status: 'green' })
expect('repo one pr failing', repo({ prs_failing_checks: 1 }), { status: 'red', status_reason: '1 pull request failing checks' })
expect('repo prs failing', repo({ prs_failing_checks: 3 }), { status: 'red', status_reason: '3 pull requests failing checks' })
expect('repo no pr failing', repo({ prs_failing_checks: 0 }), { pr_checks_failing: false, status: 'green' })
expect('repo idle and red', repo({ last_push: ago(120) }, [piped('red', 'CI failing on main')]), { days_idle: 120, archive_candidate: true, archive_candidate_level: 'amber', status: 'red', status_reason: 'CI failing on main' })
expect('repo idle and red, archive set red', repo({ last_push: ago(120), archive_candidate_severity: 'red' }, [piped('red', 'CI failing on main')]), { status: 'red', status_reason: 'idle 120 days and red: a candidate to archive' })
expect('repo idle and green', repo({ last_push: ago(120) }), { archive_candidate: false, status: 'green' })
expect('repo busy and red', repo({ last_push: ago(10) }, [piped('red', 'x')]), { archive_candidate: false })
expect('repo archived', repo({ archived: true }, [piped('red', 'x')]), { status: 'grey', status_reason: 'archived' })
expect('repo muted', repo({ muted: true, dependabot_critical: 3 }), { status: 'grey', status_reason: 'muted' })

// DORA, through the hub's aggregate(): windows, offsets, p90, and per day.
const deploy = (days: number, f: Record<string, unknown> = {}) => entity({ status: 'succeeded', deployed_at: ago(days), failed_change: false, ...f })
const service = (deployments: ReturnType<typeof entity>[]) => compute('sw_service', {}, deployments)
expect('dora nothing deployed', service([]), { dora_changes_30d: 0, dora_change_failure_rate_30d: null, dora_frequency_band: null, dora_grade: null })
const elite = Array.from({ length: 30 }, (_, i) => deploy(i + 0.5, { lead_time_hours: 2 }))
expect('dora elite', service(elite), { dora_deployments_30d: 30, dora_deploy_frequency_30d: 1, dora_frequency_band: 'elite', dora_lead_time_p50_hours: 2, dora_lead_time_band: 'elite', dora_change_failure_band: 'elite', dora_restore_band: null, dora_grade: 'elite' })
const mixed = [
  deploy(1, { lead_time_hours: 10 }),
  deploy(2, { lead_time_hours: 50 }),
  deploy(3, { lead_time_hours: 60 }),
  deploy(4, { lead_time_hours: 70 }),
  deploy(5, { status: 'failed', failed_change: true, restore_hours: 30 }),
  deploy(6, { lead_time_hours: 400 }),
  deploy(40, { lead_time_hours: 1000 }),
  deploy(45, { status: 'failed', failed_change: true, restore_hours: 2 }),
]
expect('dora mixed', service(mixed), {
  dora_deployments_30d: 5, dora_changes_30d: 6, dora_failed_changes_30d: 1, dora_change_failure_rate_30d: 16.7,
  dora_lead_time_p50_hours: 60, dora_lead_time_p90_hours: 400, dora_time_to_restore_p50_hours: 30,
  dora_deployments_prev_30d: 1, dora_changes_prev_30d: 2, dora_failed_changes_prev_30d: 1, dora_change_failure_rate_prev_30d: 50,
  dora_lead_time_p50_prev_hours: 1000, dora_time_to_restore_p50_prev_hours: 2,
  dora_frequency_band: 'high', dora_lead_time_band: 'high', dora_change_failure_band: 'low', dora_restore_band: 'medium', dora_grade: 'low',
})
expect('dora one deploy', service([deploy(3, { lead_time_hours: 200 })]), { dora_frequency_band: 'medium', dora_lead_time_band: 'medium', dora_grade: 'medium' })
expect('dora only failures', service([deploy(3, { status: 'failed', failed_change: true })]), { dora_deployments_30d: 0, dora_frequency_band: 'low', dora_change_failure_band: 'low', dora_grade: 'low' })
const share = (failedCount: number, total: number) => Array.from({ length: total }, (_, i) => deploy(i + 0.5, { failed_change: i < failedCount }))
expect('dora 5% is elite', service(share(1, 20)), { dora_change_failure_band: 'elite' })
expect('dora 10% is high', service(share(1, 10)), { dora_change_failure_band: 'high' })
expect('dora 15% is medium', service(share(3, 20)), { dora_change_failure_band: 'medium' })
expect('dora over 15% is low', service(share(4, 20)), { dora_change_failure_band: 'low' })
expect('dora restore under an hour', service([deploy(1, { status: 'failed', failed_change: true, restore_hours: 0.5 })]), { dora_restore_band: 'elite' })
expect('dora team', compute('team', {}, elite), { dora_grade: 'elite' })

console.log(`evaluated: ${cases} cases`)
if (failures > 0) {
  console.error(`${failures} failure(s)`)
  process.exit(1)
}
console.log('all expressions compile and evaluate as intended')
