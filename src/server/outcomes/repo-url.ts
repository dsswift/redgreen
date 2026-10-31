import type { ProviderKind } from '../../shared/model.ts'

/** A repository, and a folder in it, named by a browser URL in a catalog. */
export interface RepoRef {
  provider: ProviderKind
  /** As the forge names it: `owner/name`, `group/sub/project`, or `org/project/repo` for Azure DevOps. */
  fullName: string
  /** The folder the URL points into, `''` for the root. */
  path: string
}

/**
 * Reads a catalog's repository URL: `https://github.com/o/r`, with or without
 * `/tree/<branch>/<folder>`; a GitLab project, `/-/tree/<branch>/<folder>`
 * allowed; or `https://dev.azure.com/org/project/_git/repo`. `gitlabHost` is the
 * self-managed instance, if any. Null when it is none of those.
 */
export function parseRepoUrl(url: string | null | undefined, gitlabHost = 'gitlab.com'): RepoRef | null {
  if (!url) return null
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return null
  }
  const parts = u.pathname.split('/').filter(Boolean)
  const host = u.hostname.toLowerCase()
  if (host === 'github.com') {
    const [owner, repo, kind, , ...rest] = parts
    if (!owner || !repo) return null
    const name = repo.replace(/\.git$/, '')
    return { provider: 'github', fullName: `${owner}/${name}`, path: kind === 'tree' || kind === 'blob' ? rest.join('/') : '' }
  }
  if (host === 'dev.azure.com' || host.endsWith('.visualstudio.com')) {
    const git = parts.indexOf('_git')
    if (git < 1 || !parts[git + 1]) return null
    const org = host === 'dev.azure.com' ? parts[0] : host.split('.')[0]
    const project = host === 'dev.azure.com' ? parts[git - 1] : parts[git - 1]
    if (!org || !project) return null
    return { provider: 'azuredevops', fullName: `${org}/${project}/${decodeURIComponent(parts[git + 1]!)}`, path: '' }
  }
  if (host === gitlabHost.toLowerCase() || host === 'gitlab.com') {
    const dash = parts.indexOf('-')
    const projectParts = dash === -1 ? parts : parts.slice(0, dash)
    if (projectParts.length < 2) return null
    const last = projectParts[projectParts.length - 1]!.replace(/\.git$/, '')
    const fullName = [...projectParts.slice(0, -1), last].join('/')
    const kind = dash === -1 ? undefined : parts[dash + 1]
    const path = kind === 'tree' || kind === 'blob' ? parts.slice(dash + 3).join('/') : ''
    return { provider: 'gitlab', fullName, path }
  }
  return null
}

/** Two URLs that name the same repository, however they are written. */
export function sameRepo(a: RepoRef | null, b: RepoRef | null): boolean {
  return !!a && !!b && a.provider === b.provider && a.fullName.toLowerCase() === b.fullName.toLowerCase()
}
