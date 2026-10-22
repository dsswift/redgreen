import type { Octokit } from 'octokit'
import type { Logger } from '../../log.ts'
import { HUMAN_LOOKBACK, lastChangeOf, type RepoFiles } from '../../services.ts'
import { isStatus } from '../errors.ts'

/** A repository's files at the head of its default branch, or null for an empty one. Files are read as blobs by hash, so a read is cached for good. */
export async function githubFiles(kit: Octokit, fullName: string, branch: string, log: Logger): Promise<RepoFiles | null> {
  const [owner = '', repo = ''] = fullName.split('/')
  let commit: string
  try {
    const { data } = await kit.request('GET /repos/{owner}/{repo}/branches/{branch}', { owner, repo, branch })
    commit = data.commit.sha
  } catch (error) {
    if (isStatus(error, 404, 409)) return null
    throw error
  }
  const blobs = new Map<string, string>()
  return {
    commit,
    list: async () => {
      const { data } = await kit.request('GET /repos/{owner}/{repo}/git/trees/{tree_sha}', { owner, repo, tree_sha: commit, recursive: '1' })
      if (data.truncated) log.warn('file tree truncated by github; services are read from part of it', { repo: fullName, files: data.tree.length })
      for (const item of data.tree) if (item.type === 'blob' && item.path && item.sha) blobs.set(item.path, item.sha)
      return [...blobs.keys()]
    },
    read: async (path) => {
      const sha = blobs.get(path)
      if (!sha) return null
      const { data } = await kit.request('GET /repos/{owner}/{repo}/git/blobs/{file_sha}', { owner, repo, file_sha: sha })
      return data.encoding === 'base64' ? Buffer.from(data.content, 'base64').toString('utf8') : data.content
    },
    lastChange: async (path) => {
      const { data } = await kit.request('GET /repos/{owner}/{repo}/commits', { owner, repo, sha: commit, per_page: HUMAN_LOOKBACK, ...(path ? { path } : {}) })
      return lastChangeOf(data.map((c) => ({ at: c.commit.committer?.date ?? c.commit.author?.date ?? null, by: c.author?.login ?? c.commit.author?.name ?? null, email: c.commit.author?.email ?? (c.author?.type === 'Bot' ? '[bot]' : null) })))
    },
  }
}
