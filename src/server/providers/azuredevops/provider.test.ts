import { afterEach, describe, expect, it, vi } from 'vitest'
import { loadConfig } from '../../config.ts'
import { createLogger } from '../../log.ts'
import { SecretBox } from '../../secrets.ts'
import { Store } from '../../store.ts'
import { AzureDevopsProvider, organizationName } from './provider.ts'

const log = createLogger('test')

function provider(env: Record<string, string>) {
  const config = loadConfig({ PUBLIC_URL: 'https://redgreen.example.org', ...env })
  return new AzureDevopsProvider(config, new Store(':memory:'), SecretBox.load(Buffer.alloc(32, 1).toString('base64'), '/tmp', log), log)
}

const ENV = { AZURE_DEVOPS_TENANT_ID: 'tenant', AZURE_DEVOPS_CLIENT_ID: 'client', AZURE_DEVOPS_CLIENT_SECRET: 'shh' }
const INPUT = { tenantId: 'tenant', clientId: 'client', clientSecret: 'shh', organizations: ['https://dev.azure.com/Acme/', 'acme', 'Other'] }

/** Stands in for Entra and Azure DevOps. Records every URL asked for. */
function stubFetch(answer: (url: string) => Response) {
  const calls: string[] = []
  vi.stubGlobal('fetch', async (input: string | URL) => {
    calls.push(String(input))
    return answer(String(input))
  })
  return calls
}
const token = () => Response.json({ access_token: 'tok', expires_in: 3600 })

afterEach(() => vi.unstubAllGlobals())

describe('AzureDevopsProvider', () => {
  it('is unconfigured without credentials', () => {
    const p = provider({})
    expect(p.configured()).toBe(false)
    expect(p.setup()).toEqual({ app: null, organizations: [] })
  })

  it('needs an organization as well as a service principal', () => {
    expect(provider(ENV).configured()).toBe(false)
    const p = provider({ ...ENV, AZURE_DEVOPS_ORGANIZATIONS: 'Acme, https://dev.azure.com/Other/' })
    expect(p.status()).toMatchObject({ kind: 'azuredevops', configured: true, source: 'service principal (environment)', links: [{ label: 'Acme', url: 'https://dev.azure.com/Acme' }, { label: 'Other', url: 'https://dev.azure.com/Other' }] })
    expect(p.setup()).toEqual({ app: { source: 'environment', tenantId: 'tenant', clientId: 'client' }, organizations: ['Acme', 'Other'] })
  })

  it('offers each named organization as an approved account', async () => {
    const accounts = await provider({ ...ENV, AZURE_DEVOPS_ORGANIZATIONS: 'Acme' }).discoverAccounts()
    expect(accounts).toEqual([
      { id: 'azuredevops:org:acme', provider: 'azuredevops', login: 'Acme', name: 'Acme', kind: 'organization', avatarUrl: null, url: 'https://dev.azure.com/Acme', connection: { type: 'azuredevops-service-principal' }, preapproved: true },
    ])
  })

  it('refuses a service principal from the settings page when the environment provides one', async () => {
    await expect(provider(ENV).saveApp(INPUT)).rejects.toThrow(/environment/)
  })

  it('keeps a service principal from the settings page only once Entra accepts it', async () => {
    const p = provider({})
    stubFetch(() => Response.json({ error: 'invalid_client', error_description: 'AADSTS7000215: Invalid client secret provided.\nTrace ID: x' }, { status: 401 }))
    await expect(p.saveApp(INPUT)).rejects.toThrow('Entra token request failed (401): AADSTS7000215: Invalid client secret provided.')
    expect(p.configured()).toBe(false)

    const calls = stubFetch(token)
    await p.saveApp(INPUT)
    expect(calls).toEqual(['https://login.microsoftonline.com/tenant/oauth2/v2.0/token'])
    expect(p.setup()).toEqual({ app: { source: 'setup', tenantId: 'tenant', clientId: 'client' }, organizations: ['Acme', 'Other'] })

    await p.saveApp({ ...INPUT, clientSecret: '', organizations: ['Acme'] })
    expect(p.setup().organizations).toEqual(['Acme'])
    await expect(p.saveApp({ ...INPUT, clientId: 'another', clientSecret: '' })).rejects.toThrow(/secret/)
  })

  it('lists an organization on one token and names repos by organization, project, and name', async () => {
    const p = provider({ ...ENV, AZURE_DEVOPS_ORGANIZATIONS: 'Acme' })
    const calls = stubFetch((url) => (url.startsWith('https://login.microsoftonline.com/') ? token() : Response.json({ count: 1, value: [{ id: 'r1', name: 'api', webUrl: '', project: { id: 'p1', name: 'Shop' } }] })))
    const [account] = await p.discoverAccounts()
    const row = { ...account!, status: 'active' as const, lastError: null, seenAt: '' }
    expect(await p.discoverRepos(row)).toEqual([{ id: 'azuredevops:r1', provider: 'azuredevops', accountId: 'azuredevops:org:acme', fullName: 'Acme/Shop/api' }])
    await p.discoverRepos(row)
    expect(calls.filter((url) => url.startsWith('https://login.microsoftonline.com/'))).toHaveLength(1)
    expect(calls).toContain('https://dev.azure.com/Acme/_apis/git/repositories?api-version=7.1')
  })

  it('reports a sign-in redirect as the organization refusing the application', async () => {
    const p = provider({ ...ENV, AZURE_DEVOPS_ORGANIZATIONS: 'Acme' })
    stubFetch((url) => (url.startsWith('https://login.microsoftonline.com/') ? token() : new Response(null, { status: 302, headers: { location: 'https://example.org/_signin' } })))
    const [account] = await p.discoverAccounts()
    await expect(p.discoverRepos({ ...account!, status: 'active', lastError: null, seenAt: '' })).rejects.toMatchObject({ status: 401, message: expect.stringContaining('is the application a member of it?') })
  })

  it('takes an organization by name or by URL', () => {
    expect(organizationName(' Acme ')).toBe('Acme')
    expect(organizationName('https://dev.azure.com/Acme/')).toBe('Acme')
    expect(organizationName('https://dev.azure.com/Acme/Shop/_git/api')).toBe('Acme')
    expect(organizationName('https://acme.visualstudio.com/Shop')).toBe('acme')
  })
})
