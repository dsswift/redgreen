import { HUMAN_LOOKBACK, lastChangeOf, type RepoFiles } from '../../services.ts'
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
  /** A binary file's content comes base64 encoded, and says so in its metadata. */
  const item = async (path: string) => {
    try {
      return await client.get<{ content?: string; contentMetadata?: { isBinary?: boolean } }>(`${git}/items`, { path: `/${path}`, includeContent: true, $format: 'json', ...at })
    } catch (error) {
      if (isStatus(error, 404)) return null
      throw error
    }
  }
  return {
    commit,
    list: async () => (await client.list<{ path: string; isFolder?: boolean }>(`${git}/items`, { scopePath: '/', recursionLevel: 'Full', ...at })).filter((i) => !i.isFolder).map((i) => i.path.replace(/^\//, '')),
    read: async (path) => (await item(path))?.content ?? null,
    bytes: async (path) => {
      const found = await item(path)
      if (found?.content === undefined) return null
      return Buffer.from(found.content, found.contentMetadata?.isBinary ? 'base64' : 'utf8')
    },
    lastChange: async (path) => {
      const found = await client.get<{ value: { committer?: { date?: string }; author?: { name?: string; email?: string } }[] }>(`${git}/commits`, { 'searchCriteria.itemVersion.version': commit, 'searchCriteria.itemVersion.versionType': 'commit', 'searchCriteria.itemPath': `/${path}`, 'searchCriteria.$top': HUMAN_LOOKBACK })
      return lastChangeOf(found.value.map((c) => ({ at: c.committer?.date ?? null, by: c.author?.name ?? null, email: c.author?.email ?? null })))
    },
  }
}
