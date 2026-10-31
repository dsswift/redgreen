// Where incidents would come from. None exists yet: no provider reports an
// alert that fired on a service, so change failure rate and time to restore
// are judged from deployments alone. A source that can answer plugs in here
// and nowhere else; `classify` in shared/outcomes.ts already takes what it
// returns.

import type { Incident } from '../../shared/outcomes.ts'

export interface IncidentSource {
  /** Every incident opened at or after `since`, resolved or not. */
  incidents(since: string): Promise<Incident[]>
}

/** The sources registered. Empty today; `register` is the one way to add one. */
const sources: IncidentSource[] = []

export function registerIncidentSource(source: IncidentSource): void {
  sources.push(source)
}

export function incidentSourcesConfigured(): boolean {
  return sources.length > 0
}

export async function incidentsSince(since: string): Promise<Incident[]> {
  const all = await Promise.all(sources.map((s) => s.incidents(since)))
  return all.flat()
}
