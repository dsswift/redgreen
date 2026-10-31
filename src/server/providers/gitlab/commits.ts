import type { CarriedCommit } from '../../../shared/outcomes.ts'
import type { Logger } from '../../log.ts'
import { isStatus } from '../errors.ts'
import type { GitlabClient } from './client.ts'

interface GlCommit {
  id: string
  title: string
  authored_date?: string | null
  committed_date?: string | null
}

const carried = (c: GlCommit): CarriedCommit | null => {
  const authoredAt = c.authored_date ?? c.committed_date
  return authoredAt ? { sha: c.id, authoredAt, title: c.title } : null
}

/** The commits from `base` (exclusive) to `head`, oldest first, through the compare endpoint; `head` alone when there is no base. Null when GitLab does not have either commit. */
export async function gitlabCommitsBetween(client: GitlabClient, projectId: number, base: string | null, head: string, log: Logger): Promise<CarriedCommit[] | null> {
  try {
    if (base === null) {
      const one = carried(await client.get<GlCommit>(`projects/${projectId}/repository/commits/${encodeURIComponent(head)}`))
      return one ? [one] : []
    }
    const compare = await client.get<{ commits: GlCommit[] }>(`projects/${projectId}/repository/compare`, { from: base, to: head })
    return compare.commits.map(carried).filter((c): c is CarriedCommit => c !== null)
  } catch (error) {
    if (isStatus(error, 404)) {
      log.debug('commits between revisions unavailable', { project: projectId, base, head })
      return null
    }
    throw error
  }
}
