import { describe, expect, it } from 'vitest'
import { parseRepoUrl, sameRepo } from './repo-url.ts'

describe('parseRepoUrl', () => {
  it('reads a github repository, with or without a folder', () => {
    expect(parseRepoUrl('https://github.com/dsswift/redgreen')).toEqual({ provider: 'github', fullName: 'dsswift/redgreen', path: '' })
    expect(parseRepoUrl('https://github.com/spraguehouse/sh-monorepo/tree/main/workloads/spraguehouse')).toEqual({ provider: 'github', fullName: 'spraguehouse/sh-monorepo', path: 'workloads/spraguehouse' })
    expect(parseRepoUrl('https://github.com/o/r.git')).toEqual({ provider: 'github', fullName: 'o/r', path: '' })
  })

  it('reads a gitlab project, nested groups and folders included', () => {
    expect(parseRepoUrl('https://gitlab.com/group/sub/project')).toEqual({ provider: 'gitlab', fullName: 'group/sub/project', path: '' })
    expect(parseRepoUrl('https://gitlab.com/group/project/-/tree/main/apps/api')).toEqual({ provider: 'gitlab', fullName: 'group/project', path: 'apps/api' })
    expect(parseRepoUrl('https://git.example.org/g/p', 'git.example.org')).toEqual({ provider: 'gitlab', fullName: 'g/p', path: '' })
  })

  it('reads an azure devops repository', () => {
    expect(parseRepoUrl('https://dev.azure.com/SpragueHouse/Home/_git/site')).toEqual({ provider: 'azuredevops', fullName: 'SpragueHouse/Home/site', path: '' })
  })

  it('answers null for anything else', () => {
    expect(parseRepoUrl(null)).toBeNull()
    expect(parseRepoUrl('not a url')).toBeNull()
    expect(parseRepoUrl('https://charts.jetstack.io')).toBeNull()
    expect(parseRepoUrl('https://github.com/onlyowner')).toBeNull()
  })

  it('compares repositories without regard to case or folder', () => {
    expect(sameRepo(parseRepoUrl('https://github.com/A/B/tree/main/x'), parseRepoUrl('https://github.com/a/b'))).toBe(true)
    expect(sameRepo(parseRepoUrl('https://github.com/a/b'), parseRepoUrl('https://github.com/a/c'))).toBe(false)
  })
})
