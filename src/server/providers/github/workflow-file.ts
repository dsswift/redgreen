import { parse } from 'yaml'

/** Cron expressions a GitHub Actions workflow file declares under `on.schedule`. */
export function parseSchedules(workflowYaml: string): string[] {
  let doc: unknown
  try {
    doc = parse(workflowYaml)
  } catch {
    return []
  }
  if (!isRecord(doc)) return []
  const on = doc.on
  if (!isRecord(on) || !Array.isArray(on.schedule)) return []
  return on.schedule.flatMap((entry) => (isRecord(entry) && typeof entry.cron === 'string' ? [entry.cron.trim()] : []))
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
