import type { Octokit } from 'octokit'
import type { CarriedCommit } from '../../../shared/outcomes.ts'
import type { Logger } from '../../log.ts'
import { isStatus } from '../errors.ts'

/** Commits a compare answers at most. GitHub stops listing past this many; a range that long is read in part and says so. */
const COMPARE_LIMIT = 250

interface GhCommit {
  sha: string
  commit: { message: string; author: { date?: string } | null; committer: { date?: string } | null }
}

const carried = (c: GhCommit): CarriedCommit | null => {
  const authoredAt = c.commit.author?.date ?? c.commit.committer?.date
  return authoredAt ? { sha: c.sha, authoredAt, title: c.commit.message.split('\n', 1)[0] ?? '' } : null
}

/** The commits from `base` (exclusive) to `head`, oldest first, through the compare endpoint; `head` alone when there is no base. Null when GitHub does not have either commit. */
export async function githubCommitsBetween(kit: Octokit, fullName: string, base: string | null, head: string, log: Logger): Promise<CarriedCommit[] | null> {
  const [owner = '', repo = ''] = fullName.split('/')
  try {
    if (base === null) {
      const { data } = await kit.request('GET /repos/{owner}/{repo}/commits/{ref}', { owner, repo, ref: head })
      const one = carried(data as GhCommit)
      return one ? [one] : []
    }
    const { data } = await kit.request('GET /repos/{owner}/{repo}/compare/{basehead}', { owner, repo, basehead: `${base}...${head}`, per_page: COMPARE_LIMIT })
    const compare = data as { total_commits: number; commits: GhCommit[] }
    if (compare.total_commits > compare.commits.length) log.warn('compare truncated by github; lead time counts the oldest commits only', { repo: fullName, base, head, total: compare.total_commits, listed: compare.commits.length })
    return compare.commits.map(carried).filter((c): c is CarriedCommit => c !== null)
  } catch (error) {
    if (isStatus(error, 404, 422)) {
      log.debug('commits between revisions unavailable', { repo: fullName, base, head, status: Number((error as { status: number }).status) })
      return null
    }
    throw error
  }
}
