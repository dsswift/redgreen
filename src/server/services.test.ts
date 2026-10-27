import { describe, expect, it } from 'vitest'
import { SERVICE_RULES, budgeted, detectServices, emailOf, isBot, lastChangeOf, readServices, serviceFiles, type RepoFiles } from './services.ts'

/** A repository held in memory: path to contents. A path mapped to '' exists and says nothing. */
const repo = (files: Record<string, string>) => ({ paths: Object.keys(files), read: async (p: string) => files[p] ?? null })
const detect = async (files: Record<string, string>, name = 'shop') => {
  const r = repo(files)
  return (await detectServices(r.paths, r.read, name)).map((s) => `${s.id || '.'} ${s.type} ${s.language} ${s.name}`)
}

const csproj = (body: string, sdk = 'Microsoft.NET.Sdk') => `<Project Sdk="${sdk}"><PropertyGroup>${body}</PropertyGroup></Project>`

describe('services in a monorepo', () => {
  it('finds each app once, by the files that build it, and leaves out libraries and tests', async () => {
    expect(
      await detect({
        'src/Orders.Api/Orders.Api.csproj': csproj('<AzureFunctionsVersion>v4</AzureFunctionsVersion>'),
        'src/Orders.Api/host.json': '{}',
        'src/Orders.Api/Run.cs': '',
        'src/Portal/Portal.csproj': csproj('', 'Microsoft.NET.Sdk.Web'),
        'src/Portal/Pages/Index.razor': '',
        'src/Shared/Shared.csproj': csproj(''),
        'src/Orders.Api.Tests/Orders.Api.Tests.csproj': csproj('<OutputType>Exe</OutputType>'),
        'cmd/shopctl/main.go': 'package main\n\nfunc main() {}\n',
        'cmd/shopd/main.go': 'package main\n\nfunc main() { http.ListenAndServe(":80", nil) }\n',
        'web/package.json': JSON.stringify({ name: '@shop/web', dependencies: { next: '15' }, devDependencies: { typescript: '5' } }),
        'tools/report/pyproject.toml': '[project]\nname = "report"\n\n[project.scripts]\nreport = "report:main"\n',
        'samples/demo/package.json': JSON.stringify({ name: 'demo', scripts: { start: 'node .' } }),
        'infra/main.tf': 'terraform {\n  backend azurerm {}\n}\n',
        'Dockerfile': 'FROM hashicorp/terraform\n',
      }),
    ).toEqual([
      'cmd/shopctl tool Go shopctl',
      'cmd/shopd backend Go shopd',
      'src/Orders.Api backend C# Orders.Api',
      'src/Portal frontend C# Portal',
      'tools/report tool Python report',
      'web frontend TypeScript web',
      'infra infrastructure Terraform shop infrastructure',
    ])
  })

  it('makes one app of a front end and the .NET host that serves it, whichever holds the other', async () => {
    expect(
      await detect({
        'apps/s6/package.json': JSON.stringify({ dependencies: { '@angular/core': '20' } }),
        'apps/s6/tsconfig.json': '{}',
        'apps/s6/bff/Bff.csproj': csproj('', 'Microsoft.NET.Sdk.Web'),
      }),
    ).toEqual(['apps/s6 frontend TypeScript s6'])
  })

  it('reads a Functions host that holds other apps as their shared settings, not an app', async () => {
    expect(await detect({ 'host.json': '{}', 'a/A.csproj': csproj('<AzureFunctionsVersion>v4</AzureFunctionsVersion>'), 'a/host.json': '{}' })).toEqual(['a backend C# A'])
  })

  it('names a lone Go program after its repository and calls it a backend when a Dockerfile runs it', async () => {
    expect(await detect({ 'main.go': 'package main\nfunc main() {}\n', 'Dockerfile': 'FROM golang' }, 'pusher')).toEqual(['. backend Go pusher'])
  })

  it('gives infrastructure its own id when an app shares its folder', async () => {
    expect(await detect({ 'package.json': JSON.stringify({ scripts: { start: 'node server.js' } }), 'main.bicep': '' })).toEqual(['. backend JavaScript shop', 'infrastructure infrastructure Bicep shop infrastructure'])
  })
})

describe('a repository with nothing that runs', () => {
  it('is one infrastructure service when it is only a landing zone', async () => {
    expect(await detect({ 'terraform/main.tf': '', 'terraform/prod.tfvars': '', 'README.md': '' }, 'az-lz-shop')).toEqual(['terraform infrastructure Terraform az-lz-shop'])
  })

  it('is a library when its Terraform has no root to apply', async () => {
    expect(await detect({ 'modules/vault/main.tf': 'resource "x" "y" {}', 'modules/tags/variables.tf': '' }, 'tfhub')).toEqual(['modules library Terraform tfhub'])
  })

  it('is a library when it only builds one', async () => {
    expect(await detect({ 'Checks.csproj': csproj(''), 'Checks.cs': '' }, 'checks')).toEqual(['. library C# checks'])
  })

  it('is infrastructure when it only holds Kubernetes manifests', async () => {
    expect(await detect({ 'apps/kustomization.yaml': '', 'apps/argo/kustomization.yaml': '', '_images/shell/Dockerfile': 'FROM node' }, 'k8s')).toEqual(['apps infrastructure null k8s'])
  })

  it('is one tool when it holds scripts, and nothing when it is mostly prose', async () => {
    expect(await detect({ 'scripts/a.ps1': '', 'scripts/b.ps1': '', 'README.md': '' }, 'defaults')).toEqual(['. tool PowerShell defaults'])
    expect(await detect({ 'a.md': '', 'b.md': '', 'c.md': '', 'd.md': '', 'toc.js': '' }, 'notes')).toEqual([])
    expect(await detect({ 'README.md': '' }, 'empty')).toEqual([])
  })
})

describe('what each service carries in its own folder', () => {
  const carried = async (files: Record<string, string>, name = 'shop') => {
    const r = repo(files)
    return Object.fromEntries((await detectServices(r.paths, r.read, name)).map((s) => [s.id || '.', s.files]))
  }
  const fn = csproj('<AzureFunctionsVersion>v4</AzureFunctionsVersion>')

  it('scores each service in a monorepo on its own README, Dockerfile, and tests', async () => {
    expect(
      await carried({
        'README.md': '',
        'workloads/projects/README.md': '',
        'workloads/projects/Api/Api.csproj': fn,
        'workloads/projects/Api/Dockerfile': '',
        'workloads/projects/Tests.Api/Tests.Api.csproj': csproj('<IsTestProject>true</IsTestProject>'),
        'workloads/projects/Tests.Api/HandlerTests.cs': '',
        'workloads/billing/Ingestor/Ingestor.csproj': fn,
        'workloads/billing/Ingestor/README.md': '',
        'workloads/web/console/Console.csproj': csproj('', 'Microsoft.NET.Sdk.Web'),
        'workloads/web/console/Dockerfile': '',
      }),
    ).toEqual({
      'workloads/billing/Ingestor': { readme: true, dockerfile: false, tests: false },
      'workloads/projects/Api': { readme: true, dockerfile: true, tests: true },
      'workloads/web/console': { readme: false, dockerfile: true, tests: false },
    })
  })

  it('gives a test project to the service it is named for, either way round', async () => {
    const services = [
      { id: 'src/Orders.Api', path: 'src/Orders.Api', name: 'Orders.Api', type: 'backend' as const, language: 'C#' as const },
      { id: 'src/Billing', path: 'src/Billing', name: 'Billing', type: 'backend' as const, language: 'C#' as const },
    ]
    const none = async () => null
    expect((await serviceFiles(['test/Orders.Api.UnitTests/OrderTests.cs'], services, none)).map((f) => f.tests)).toEqual([true, false])
    expect((await serviceFiles(['test/Tests.Billing/BillTests.cs'], services, none)).map((f) => f.tests)).toEqual([false, true])
  })

  it('finds tests by the file names each language uses', async () => {
    const tool = async (path: string) => (await serviceFiles([path], [{ id: '', path: '', name: 'x', type: 'tool', language: null }], async () => null))[0]!.tests
    expect(await Promise.all(['sync/run_test.go', 'src/app.test.ts', 'src/app.spec.tsx', 'pkg/test_sync.py', 'src/app.ts', 'docs/testing.md'].map(tool))).toEqual([true, true, true, true, false, false])
  })

  it('gives a lone service the README at the root, and leaves its Terraform out of the count', async () => {
    expect(await carried({ 'README.md': '', 'Dockerfile': 'FROM golang', 'main.go': 'package main\nfunc main() {}\n', 'main_test.go': '', 'infra/main.tf': 'terraform {\n  backend azurerm {}\n}\n' }, 'pusher')).toEqual({
      '.': { readme: true, dockerfile: true, tests: true },
      infra: { readme: false, dockerfile: false, tests: false },
    })
  })

  it('gives a Dockerfile above several services to the ones it copies, and a README there to none', async () => {
    expect(
      await carried({
        'web/README.md': '',
        'web/Dockerfile': 'COPY Portal/Portal.csproj Portal/\nCOPY . .\n',
        'web/Portal/Portal.csproj': csproj('', 'Microsoft.NET.Sdk.Web'),
        'web/Admin/Admin.csproj': csproj('<OutputType>Exe</OutputType>'),
      }),
    ).toEqual({ 'web/Admin': { readme: false, dockerfile: false, tests: false }, 'web/Portal': { readme: false, dockerfile: true, tests: false } })
  })

  it('gives a Dockerfile held by one service to the services below it that it builds', async () => {
    expect(
      await carried({
        'package.json': JSON.stringify({ scripts: { start: 'node .' } }),
        'go.Dockerfile': 'COPY cmd ./cmd\nRUN go build -o /out/agent ./cmd/agent\n',
        'cmd/agent/main.go': 'package main\nfunc main() {}\n',
        'cmd/admin/main.go': 'package main\nfunc main() {}\n',
      }),
    ).toEqual({ '.': { readme: false, dockerfile: true, tests: false }, 'cmd/admin': { readme: false, dockerfile: false, tests: false }, 'cmd/agent': { readme: false, dockerfile: true, tests: false } })
  })

  it('does not take a README inside a service, such as a bundled library, for the service', async () => {
    expect(await carried({ 'site/Site.csproj': csproj('', 'Microsoft.NET.Sdk.Web'), 'site/wwwroot/lib/README.md': '' })).toEqual({ site: { readme: false, dockerfile: false, tests: false } })
  })

  it('counts nothing a dependency folder brings in', async () => {
    expect(await carried({ 'package.json': JSON.stringify({ scripts: { start: 'node .' } }), 'node_modules/lib/README.md': '', 'node_modules/lib/lib.test.js': '' })).toEqual({ '.': { readme: false, dockerfile: false, tests: false } })
  })
})

describe('reading a repository', () => {
  const files = (commit: string, reads: string[] = []): RepoFiles => ({
    commit,
    list: async () => ['main.go'],
    read: async (p) => {
      reads.push(p)
      return 'package main\nfunc main() {}\n'
    },
    lastChange: async (p) => (p === '' ? { at: `${commit}-at`, by: `${commit}-by`, email: `${commit}@bot.example`, humanAt: `${commit}-hat`, humanBy: `${commit}-hby`, humanEmail: `${commit}@dev.example` } : null),
  })

  it('stamps each service with when and by whom its own folder last changed', async () => {
    const { services } = await readServices(files('a'), 'tool', null)
    expect(services.services[0]).toMatchObject({ changedAt: 'a-at', changedBy: 'a-by', changedByEmail: 'a@bot.example', humanChangedAt: 'a-hat', humanChangedBy: 'a-hby', humanChangedByEmail: 'a@dev.example' })
  })

  it('reads again only when the branch moved or the rules changed', async () => {
    const reads: string[] = []
    const first = await readServices(files('a', reads), 'tool', null)
    expect(first).toMatchObject({ reused: false, services: { commit: 'a', rules: SERVICE_RULES, services: [{ id: '', type: 'tool', language: 'Go' }] } })
    expect((await readServices(files('a', reads), 'tool', first.services)).reused).toBe(true)
    expect((await readServices(files('b', reads), 'tool', first.services)).reused).toBe(false)
    expect((await readServices(files('a', reads), 'tool', { ...first.services, rules: SERVICE_RULES - 1 })).reused).toBe(false)
    expect(reads).toHaveLength(3)
  })

  it('has no services and nothing to read for a repository with no commits', async () => {
    expect(await readServices(null, 'x', null)).toEqual({ services: { commit: null, rules: SERVICE_RULES, services: [] }, reused: false, read: 0 })
  })

  it('stops reading at the budget', async () => {
    const r = budgeted(async () => 'x', 2)
    expect([await r.read('a'), await r.read('b'), await r.read('c')]).toEqual(['x', 'x', null])
  })
})

describe('lastChangeOf', () => {
  it('skips bot commits for the human pair and keeps them for the newest pair', () => {
    const out = lastChangeOf([
      { at: '3', by: 'release-damnit' },
      { at: '2', by: 'dependabot[bot]' },
      { at: '1', by: 'a-developer', email: ' A.Developer@Example.com ' },
    ])
    expect(out).toEqual({ at: '3', by: 'release-damnit', email: null, humanAt: '1', humanBy: 'a-developer', humanEmail: 'a.developer@example.com' })
  })
  it('keeps an address only when it is one, lowercase', () => {
    expect(emailOf('Dev@Example.COM')).toBe('dev@example.com')
    expect(emailOf('not an address')).toBeNull()
    expect(emailOf(undefined)).toBeNull()
  })
  it('leaves the human pair empty when every recent commit is a bot, and answers null with no commits', () => {
    expect(lastChangeOf([{ at: '1', by: 'x', email: 'ci[bot]@example.com' }])).toMatchObject({ humanAt: null, humanBy: null })
    expect(lastChangeOf([])).toBeNull()
  })
  it('takes extra bot names from the list given', () => {
    expect(isBot({ at: '1', by: 'Deploy Robot' }, ['deploy robot'])).toBe(true)
    expect(isBot({ at: '1', by: 'Deploy Robot' }, [])).toBe(false)
  })
})
