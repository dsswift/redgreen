import { parse } from 'yaml'
import type { Schedule } from '../../../shared/model.ts'

/** Schedules a GitHub Actions workflow file declares under `on.schedule`. GitHub evaluates them in UTC. */
export function parseSchedules(workflowYaml: string): Schedule[] {
  let doc: unknown
  try {
    doc = parse(workflowYaml)
  } catch {
    return []
  }
  if (!isRecord(doc)) return []
  const on = doc.on
  if (!isRecord(on) || !Array.isArray(on.schedule)) return []
  return on.schedule.flatMap((entry) => (isRecord(entry) && typeof entry.cron === 'string' ? [{ cron: entry.cron.trim(), timezone: 'UTC' }] : []))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
