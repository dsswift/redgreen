import { parse } from 'yaml'
import type { Schedule } from '../../../shared/model.ts'

export interface FileSchedules {
  /** Schedules that fire whether or not the branch changed. Azure Pipelines evaluates them in UTC. */
  schedules: Schedule[]
  /** Schedules that fire only after a change. A quiet branch skips them, so a missing run says nothing. */
  conditional: number
}

/** Schedules an Azure Pipelines YAML file declares under `schedules`. */
export function parseSchedules(pipelineYaml: string): FileSchedules {
  let doc: unknown
  try {
    doc = parse(pipelineYaml)
  } catch {
    return { schedules: [], conditional: 0 }
  }
  if (!isRecord(doc) || !Array.isArray(doc.schedules)) return { schedules: [], conditional: 0 }
  const entries = doc.schedules.filter((entry): entry is Record<string, unknown> & { cron: string } => isRecord(entry) && typeof entry.cron === 'string')
  const always = entries.filter((entry) => entry.always === true)
  return { schedules: always.map((entry) => ({ cron: entry.cron.trim(), timezone: 'UTC' })), conditional: entries.length - always.length }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
