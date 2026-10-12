import type { RepoFiles } from '../../services.ts'
import { isStatus } from '../errors.ts'
import type { AzureDevopsClient } from './client.ts'
import { AZURE_DEVOPS_URL } from './repo-sync.ts'

/** A repository's files at the head of its default branch, or null for one with no commits on it. The whole tree comes in one call. */
export async function azureDevopsFiles(client: AzureDevopsClient, org: string, repoId: string, branch: string): Promise<RepoFiles | null> {
  const git = `${AZURE_DEVOPS_URL}/${encodeURIComponent(org)}/_apis/git/repositories/${repoId}`
  let commit: string | undefined
  try {
    commit = (await client.get<{ value: { commitId: string }[] }>(`${git}/commits`, { 'searchCriteria.itemVersion.version': branch, 'searchCriteria.$top': 1 })).value[0]?.commitId
  } catch (error) {
    if (isStatus(error, 404)) return null
    throw error
  }
  if (!commit) return null
  const at = { 'versionDescriptor.version': commit, 'versionDescriptor.versionType': 'commit' }
  return {
    commit,
    list: async () => (await client.list<{ path: string; isFolder?: boolean }>(`${git}/items`, { scopePath: '/', recursionLevel: 'Full', ...at })).filter((i) => !i.isFolder).map((i) => i.path.replace(/^\//, '')),
    read: async (path) => {
      try {
        return (await client.get<{ content?: string }>(`${git}/items`, { path: `/${path}`, includeContent: true, $format: 'json', ...at })).content ?? null
      } catch (error) {
        if (isStatus(error, 404)) return null
        throw error
      }
    },
    lastChange: async (path) => {
      const found = await client.get<{ value: { committer?: { date?: string } }[] }>(`${git}/commits`, { 'searchCriteria.itemVersion.version': commit, 'searchCriteria.itemVersion.versionType': 'commit', 'searchCriteria.itemPath': `/${path}`, 'searchCriteria.$top': 1 })
      return found.value[0]?.committer?.date ?? null
    },
  }
}
