import type { RepoFiles } from '../../services.ts'
import { isStatus } from '../errors.ts'
import type { GitlabClient } from './client.ts'

/** A project's files at the head of its default branch, or null for a project with no commits on it. Read at the commit, so every answer stays cached until the branch moves. */
export async function gitlabFiles(client: GitlabClient, projectId: number, branch: string): Promise<RepoFiles | null> {
  let commit: string
  try {
    commit = (await client.get<{ commit: { id: string } }>(`projects/${projectId}/repository/branches/${encodeURIComponent(branch)}`)).commit.id
  } catch (error) {
    if (isStatus(error, 404)) return null
    throw error
  }
  return {
    commit,
    list: async () => (await client.paginate<{ path: string; type: string }>(`projects/${projectId}/repository/tree`, { ref: commit, recursive: true })).filter((i) => i.type === 'blob').map((i) => i.path),
    read: async (path) => {
      try {
        const file = await client.get<{ content: string; encoding: string }>(`projects/${projectId}/repository/files/${encodeURIComponent(path)}`, { ref: commit })
        return file.encoding === 'base64' ? Buffer.from(file.content, 'base64').toString('utf8') : file.content
      } catch (error) {
        if (isStatus(error, 404)) return null
        throw error
      }
    },
  }
}
