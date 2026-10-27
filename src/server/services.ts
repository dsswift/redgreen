// Finds the services a repository holds from the files that build them: a
// .NET project that is an app, a package.json that starts one, a Go main
// package, a Functions host, a Dockerfile, a Terraform or Bicep root. Every
// forge hands over the same two things, the file list at a commit and a way
// to read one file, so the rules here are the only place a service is
// recognised. The answer depends on nothing but the files, so the same
// commit always gives the same services.

import { languageOfFile, mainLanguage, type Language } from '../shared/languages.ts'
import type { DetectedService, RepoServices, ServiceFiles, ServiceType } from '../shared/model.ts'

/** Bump on any change to the rules below, so every repository is read again instead of trusting its last answer. */
export const SERVICE_RULES = 7

/** A commit as a forge names it. Either part is null when the forge left it out. */
export interface Commit {
  at: string | null
  by: string | null
  email?: string | null
}

/** The newest commit that touched a folder, and the newest of those made by a person. Either part is null when the forge left it out. */
export interface LastChange {
  at: string | null
  by: string | null
  email: string | null
  humanAt: string | null
  humanBy: string | null
  humanEmail: string | null
}

/** Commits read per folder while looking for the newest one a person made. */
export const HUMAN_LOOKBACK = 30

/** `.invalid` is a domain that can never be real (RFC 2606), so an address on it is a job signing its own commits. */
const BOT_PATTERN = /@[^@\s]+\.invalid$|\[bot\]|(^|[-_ ])bot$|^bot[-_ ]|release-damnit|github-actions|dependabot|renovate|project_\d+_bot|^gitlab-?ci/i

/** Whether a commit came from automation: a release tool, a dependency updater, a CI job. `BOT_AUTHORS` adds comma separated names or addresses. */
export function isBot(commit: Commit, extra: string[] = (process.env.BOT_AUTHORS ?? '').split(',').map((x) => x.trim().toLowerCase()).filter(Boolean)): boolean {
  const names = [commit.by, commit.email].filter((x): x is string => !!x)
  return names.some((n) => BOT_PATTERN.test(n) || extra.includes(n.toLowerCase()))
}

/** The newest commit, and the newest one a person made, from commits listed newest first. Null when there are none. */
export function lastChangeOf(commits: Commit[]): LastChange | null {
  const [newest] = commits
  if (!newest) return null
  const human = commits.find((c) => !isBot(c))
  return { at: newest.at, by: newest.by, email: emailOf(newest.email), humanAt: human?.at ?? null, humanBy: human?.by ?? null, humanEmail: emailOf(human?.email) }
}

/** An address as the hub matches it: lowercase and trimmed. Anything that is not an address is null. */
export function emailOf(v: string | null | undefined): string | null {
  const s = v?.trim().toLowerCase()
  return s && /^[^@\s]+@[^@\s]+$/.test(s) ? s : null
}

/** A repository's files at the head of its default branch. */
export interface RepoFiles {
  commit: string
  /** Every file path, relative to the root, `/` separated. */
  list(): Promise<string[]>
  /** One file's text, or null when it cannot be read. */
  read(path: string): Promise<string | null>
  /** When the newest commits at or before this one touched `path`, a folder or `''` for the root, and who made the newest and the newest by a person. Null when the forge cannot say. */
  lastChange(path: string): Promise<LastChange | null>
}

/** Files read per repository at most. A monorepo past this is read in part; the services found so far stand. */
export const READ_BUDGET = 400

/** Folders that hold tests, samples, docs, dependencies, or build output, never a service. */
const SKIPPED = new Set([
  'test', 'tests', '__tests__', '__mocks__', 'testdata', 'fixtures', 'e2e', 'spec', 'specs',
  'sample', 'samples', 'example', 'examples', 'benchmarks',
  'vendor', 'node_modules', 'third_party', 'bower_components', 'packages-cache',
  'dist', 'build', 'bin', 'obj', 'out', 'target', '.terraform',
  'docs', 'doc', 'archive', 'archives',
  '.git', '.github', '.gitlab', '.vscode', '.idea', '.venv', 'venv', '__pycache__', '.obsidian',
])
const TEST_PROJECT = /(^|[.\-_])(unit|integration|functional|acceptance|e2e)?tests?$/i

/** The kinds of build file, strongest first: where two claim one folder, the first wins it. */
const MARKERS = ['dotnet', 'go', 'rust', 'swift', 'python', 'node', 'functions', 'docker'] as const
type Marker = (typeof MARKERS)[number]

interface Found {
  path: string
  name: string
  type: ServiceType
  language: Language | null
  marker: Marker
}

/** A build file that makes a library, not something that runs. Only counts when a repository has nothing that runs. */
interface LibraryMark {
  path: string
  language: Language | null
}

const dirOf = (path: string) => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '')
const baseOf = (path: string) => path.slice(path.lastIndexOf('/') + 1)
const within = (path: string, dir: string) => dir === '' || path === dir || path.startsWith(`${dir}/`)
/** A folder named for tests, samples, or output, or one a leading `.` or `_` marks as tooling or parked. */
const skipped = (path: string) => path.split('/').slice(0, -1).some((part) => SKIPPED.has(part.toLowerCase()) || TEST_PROJECT.test(part) || /^[._]/.test(part))

/** The deepest folder every path sits in. */
function commonDir(paths: string[]): string {
  if (paths.length === 0) return ''
  let common = dirOf(paths[0]!).split('/')
  for (const p of paths.slice(1)) {
    const parts = dirOf(p).split('/')
    let i = 0
    while (i < common.length && common[i] === parts[i]) i++
    common = common.slice(0, i)
  }
  return common.join('/')
}

/** The language most of the code under a folder is written in. */
function languageUnder(files: string[], dir: string): Language | null {
  const counts: Record<string, number> = {}
  for (const f of files) {
    if (!within(f, dir)) continue
    const lang = languageOfFile(f)
    if (lang) counts[lang] = (counts[lang] ?? 0) + 1
  }
  return mainLanguage(counts)
}

type Read = (path: string) => Promise<string | null>

/** Every service in one repository's files, each with what sits beside it. `repoName` names a service that sits at the root and names nothing itself. */
export async function detectServices(paths: string[], read: Read, repoName: string): Promise<DetectedService[]> {
  const services = await findServices(paths, read, repoName)
  const files = await serviceFiles(paths, services, read)
  return services.map((s, i) => ({ ...s, files: files[i]! }))
}

async function findServices(paths: string[], read: Read, repoName: string): Promise<DetectedService[]> {
  const files = paths.filter((p) => !skipped(p)).sort()
  const named = (dir: string) => (dir ? baseOf(dir) : repoName)
  const found: Found[] = []
  const libraries: LibraryMark[] = []
  const add = (f: Found) => found.push(f)

  // .NET: a project is an app when it is a web app, a Functions app, a worker, or an exe.
  for (const file of files.filter((f) => /\.(cs|fs|vb)proj$/i.test(f))) {
    const dir = dirOf(file)
    const project = baseOf(file).replace(/\.\w+proj$/i, '')
    if (TEST_PROJECT.test(project)) continue
    const xml = await read(file)
    if (xml === null || /Microsoft\.NET\.Test\.Sdk|<IsTestProject>\s*true/i.test(xml)) continue
    const name = xml.match(/<AssemblyName>\s*([^<\s]+)\s*</)?.[1] ?? project
    const language: Language = /\.csproj$/i.test(file) ? 'C#' : 'Other'
    const pages = files.some((f) => within(f, dir) && /\.(razor|cshtml|aspx)$/i.test(f))
    let type: ServiceType | null = null
    if (/<AzureFunctionsVersion>/i.test(xml)) type = 'backend'
    else if (/Sdk="Microsoft\.NET\.Sdk\.BlazorWebAssembly"/i.test(xml)) type = 'frontend'
    else if (/Sdk="Microsoft\.NET\.Sdk\.Web"/i.test(xml)) type = pages ? 'frontend' : 'backend'
    else if (/349c5851-65df-11da-9384-00065b846f21/i.test(xml) || files.includes(`${dir ? `${dir}/` : ''}Web.config`)) type = 'frontend'
    else if (/Sdk="Microsoft\.NET\.Sdk\.Worker"/i.test(xml)) type = 'backend'
    else if (/<OutputType>\s*(Win)?Exe\s*</i.test(xml)) type = 'tool'
    if (type) add({ path: dir, name, type, language, marker: 'dotnet' })
    else libraries.push({ path: dir, language })
  }

  // Go: every main package. One that serves, or the only one with a Dockerfile to run it, is a backend.
  const goMains: { dir: string; text: string }[] = []
  for (const file of files.filter((f) => baseOf(f) === 'main.go' || (/(^|\/)cmd\/[^/]+\/[^/]+\.go$/.test(f) && !f.endsWith('_test.go')))) {
    const dir = dirOf(file)
    if (goMains.some((m) => m.dir === dir)) continue
    const text = await read(file)
    if (text && /^package main\b/m.test(text) && /\bfunc main\(/.test(text)) goMains.push({ dir, text })
  }
  const dockerized = files.some((f) => /(^|\/)Dockerfile$|\.dockerfile$/i.test(f))
  for (const { dir, text } of goMains) {
    const serves = /ListenAndServe|http\.Server\{|grpc\.NewServer/.test(text)
    add({ path: dir, name: named(dir), type: serves || (goMains.length === 1 && dockerized) ? 'backend' : 'tool', language: 'Go', marker: 'go' })
  }
  if (goMains.length === 0) for (const mod of files.filter((f) => baseOf(f) === 'go.mod')) libraries.push({ path: dirOf(mod), language: 'Go' })

  // Rust: a crate with a binary.
  for (const file of files.filter((f) => baseOf(f) === 'Cargo.toml')) {
    const dir = dirOf(file)
    const toml = await read(file)
    if (toml === null || (/^\[workspace\]/m.test(toml) && !/^\[package\]/m.test(toml))) continue
    const name = toml.match(/^name\s*=\s*"([^"]+)"/m)?.[1] ?? named(dir)
    if (/^\[\[bin\]\]/m.test(toml) || files.includes(`${dir ? `${dir}/` : ''}src/main.rs`)) {
      add({ path: dir, name, type: /^\s*(actix-web|axum|rocket|warp|hyper|tonic)\s*=/m.test(toml) ? 'backend' : 'tool', language: 'Rust', marker: 'rust' })
    } else libraries.push({ path: dir, language: 'Rust' })
  }

  // Swift: an Xcode app, or a package with an executable.
  const xcode = new Set(files.map((f) => f.match(/^(.*?)\/?[^/]+\.xcodeproj\//)?.[1]).filter((d): d is string => d !== undefined))
  for (const dir of xcode) add({ path: dir, name: named(dir), type: 'frontend', language: 'Swift', marker: 'swift' })
  for (const file of files.filter((f) => baseOf(f) === 'Package.swift')) {
    const dir = dirOf(file)
    if (xcode.has(dir)) continue
    const text = await read(file)
    if (text && /executableTarget/.test(text)) add({ path: dir, name: text.match(/name:\s*"([^"]+)"/)?.[1] ?? named(dir), type: 'tool', language: 'Swift', marker: 'swift' })
    else libraries.push({ path: dir, language: 'Swift' })
  }

  // Python: a project that serves or installs a command, a Django site, or a setup.py with console scripts.
  for (const file of files.filter((f) => ['pyproject.toml', 'setup.py', 'manage.py'].includes(baseOf(f)))) {
    const dir = dirOf(file)
    if (found.some((f) => f.marker === 'python' && f.path === dir)) continue
    const text = await read(file)
    if (text === null) continue
    const name = text.match(/^name\s*=\s*["']([^"']+)["']/m)?.[1] ?? text.match(/\bname\s*=\s*["']([^"']+)["']/)?.[1] ?? named(dir)
    const serves = baseOf(file) === 'manage.py' || /["'\s](fastapi|flask|django|uvicorn|gunicorn|starlette|aiohttp|azure-functions)\b/i.test(text)
    const command = /^\[(project|tool\.poetry)\.scripts\]/m.test(text) || /console_scripts/.test(text)
    if (serves || command) add({ path: dir, name, type: serves ? 'backend' : 'tool', language: 'Python', marker: 'python' })
    else libraries.push({ path: dir, language: 'Python' })
  }

  // Node: a package that is a web app, a server, or a command. A workspace root only gathers others.
  for (const file of files.filter((f) => baseOf(f) === 'package.json')) {
    const dir = dirOf(file)
    let pkg: { name?: string; bin?: unknown; workspaces?: unknown; scripts?: Record<string, string>; dependencies?: Record<string, string>; devDependencies?: Record<string, string> }
    try {
      pkg = JSON.parse((await read(file)) ?? 'null') ?? {}
    } catch {
      continue
    }
    if (pkg.workspaces) continue
    const deps = { ...pkg.dependencies, ...pkg.devDependencies }
    const has = (...names: string[]) => names.some((n) => n in deps)
    const name = pkg.name?.replace(/^@[^/]+\//, '') || named(dir)
    const language: Language = has('typescript') || files.includes(`${dir ? `${dir}/` : ''}tsconfig.json`) ? 'TypeScript' : 'JavaScript'
    const frontend = has('next', 'nuxt', '@angular/core', 'react-scripts', 'astro', 'gatsby', '@sveltejs/kit', '@remix-run/react', 'electron') || (has('vite') && has('react', 'vue', 'svelte', 'solid-js', 'preact', 'lit'))
    const backend = has('express', 'hono', 'fastify', 'koa', '@nestjs/core', '@hapi/hapi', '@azure/functions', 'restify') || !!pkg.scripts?.start
    const type: ServiceType | null = frontend ? 'frontend' : backend ? 'backend' : pkg.bin ? 'tool' : null
    if (type) add({ path: dir, name, type, language, marker: 'node' })
    else libraries.push({ path: dir, language })
  }

  // Azure Functions: a host.json, or a lone function.json from before hosts were required.
  const hosts = files.filter((f) => baseOf(f) === 'host.json').map(dirOf)
  for (const dir of hosts) add({ path: dir, name: named(dir), type: 'backend', language: languageUnder(files, dir), marker: 'functions' })
  for (const file of files.filter((f) => baseOf(f) === 'function.json')) {
    const fn = dirOf(file)
    if (hosts.some((h) => within(fn, h))) continue
    add({ path: fn, name: named(fn), type: 'backend', language: languageUnder(files, fn), marker: 'functions' })
  }

  // A Dockerfile nothing else explains runs something of its own. One that installs Terraform only runs the infrastructure.
  for (const file of files.filter((f) => /(^|\/)Dockerfile$/i.test(f))) {
    const dir = dirOf(file)
    if (found.some((f) => within(f.path, dir) || within(dir, f.path))) continue
    const text = await read(file)
    if (text === null || /terraform/i.test(text)) continue
    add({ path: dir, name: named(dir), type: 'backend', language: languageUnder(files, dir), marker: 'docker' })
  }

  const apps = merge(found)
  const services: DetectedService[] = apps.map((f) => ({ id: f.path, path: f.path, name: f.name, type: f.type, language: f.language }))

  // Infrastructure: a Terraform root that keeps state, or Bicep templates. Terraform with no root is a module library.
  const terraform = files.filter((f) => f.endsWith('.tf'))
  const bicep = files.filter((f) => f.endsWith('.bicep'))
  let infra: { kind: 'infrastructure' | 'library'; files: string[]; language: Language } | null = null
  if (terraform.length > 0 || bicep.length > 0) {
    const language: Language = bicep.length > terraform.length ? 'Bicep' : 'Terraform'
    const deployed = bicep.some((f) => !/(^|\/)modules\//.test(f)) || (await terraformRoot(files, terraform, read))
    infra = { kind: deployed ? 'infrastructure' : 'library', files: [...terraform, ...bicep], language }
  } else if (services.length === 0) {
    const k8s = files.filter((f) => /(^|\/)(kustomization\.ya?ml|Chart\.yaml)$/.test(f))
    if (k8s.length > 0) infra = { kind: 'infrastructure', files: k8s, language: 'Other' }
  }
  if (infra) {
    const path = commonDir(infra.files)
    const alone = services.length === 0
    const id = services.some((s) => s.id === path) ? `${path ? `${path}/` : ''}infrastructure` : path
    services.push({ id, path, name: alone ? repoName : `${repoName} infrastructure`, type: infra.kind, language: infra.language === 'Other' ? null : infra.language })
  }
  if (services.length > 0) return services

  // Nothing runs. Libraries make one library; scripts in a repository that is not mostly prose make one tool.
  if (libraries.length > 0) {
    const counts: Record<string, number> = {}
    for (const l of libraries) if (l.language) counts[l.language] = (counts[l.language] ?? 0) + 1
    const path = commonDir(libraries.map((l) => `${l.path ? `${l.path}/` : ''}x`))
    return [{ id: path, path, name: path ? baseOf(path) : repoName, type: 'library', language: mainLanguage(counts) }]
  }
  const code = files.filter((f) => languageOfFile(f) && !['HTML', 'CSS', 'SQL'].includes(languageOfFile(f)!))
  const prose = files.filter((f) => /\.(md|mdx|txt|rst|adoc)$/i.test(f)).length
  if (code.length > 0 && prose <= code.length * 3) return [{ id: '', path: '', name: repoName, type: 'tool', language: languageUnder(code, '') }]
  return []
}

/** Folders a repository holds other people's code or build output in. Nothing in them is the repository's own. */
const FOREIGN = new Set(['node_modules', 'vendor', 'third_party', 'bower_components', 'dist', 'build', 'bin', 'obj', 'out', 'target', '.git', '.terraform'])
/** A folder of tests, or a test project named for what it tests: `tests`, `__tests__`, `e2e`, `Tests.Orders.Api`, `Orders.Api.UnitTests`. */
const TEST_DIR = /^(tests?|__tests__|e2e|specs?|(unit|integration|functional|acceptance)[.\-_]?tests?)$|^tests?[.\-_]|[.\-_](unit|integration|functional|acceptance|e2e)?tests?$/i
/** A test file by its own name, in the conventions of each language. */
const TEST_FILE = /(_test\.go|\.(test|spec)\.[cm]?[jt]sx?|(^|\/)test_[^/]*\.py|_test\.py|Tests?\.(cs|swift))$/
const README = /^readme(\.|$)/i
const DOCKERFILE = /^dockerfile$|\.dockerfile$/i

/** What a test project's name says it tests: `Tests.Orders.Api` and `Orders.Api.Tests` both test `Orders.Api`. A bare `tests` folder names nothing. */
const testedName = (folder: string) =>
  /^(tests?|__tests__|e2e|specs?)$/i.test(folder) ? '' : folder.replace(/^tests?[.\-_]/i, '').replace(/[.\-_](unit|integration|functional|acceptance|e2e)?tests?$/i, '').toLowerCase()

/**
 * Which service each README, Dockerfile, and test belongs to, so a monorepo
 * scores each of its services on its own files.
 *
 * A README documents the service whose folder holds it directly, or every
 * service below it while that folder holds no other: a monorepo's root
 * README, or a workload folder's README over two apps, documents no single
 * one. A README deeper inside a service describes that subfolder, often a
 * bundled library, and is not the service's.
 *
 * A Dockerfile builds the deepest service whose folder holds it, and any
 * service below it that it names by folder, which is how one Dockerfile
 * says which of several apps it ships. One in a folder above a single
 * service builds that service.
 *
 * Tests belong to the deepest service whose folder holds them. A test
 * project named for a service, such as `Tests.Orders.Api`, belongs to it
 * wherever it sits. Infrastructure is left out of the sharing: an app and
 * the Terraform it stands on are one thing to document.
 */
export async function serviceFiles(paths: string[], services: DetectedService[], read: Read): Promise<ServiceFiles[]> {
  const result: ServiceFiles[] = services.map(() => ({ readme: false, dockerfile: false, tests: false }))
  const apps = services.map((s, i) => ({ s, i })).filter(({ s }) => s.type !== 'infrastructure')
  /** The deepest service whose folder holds the path. */
  const holder = (path: string): number | null => {
    const holders = services.map((s, i) => ({ s, i })).filter(({ s }) => within(path, s.path))
    return holders.length > 0 ? holders.reduce((a, b) => (b.s.path.length > a.s.path.length ? b : a)).i : null
  }
  /** The services the nearest folder above `dir` that holds any of them holds. */
  const below = (dir: string) => {
    for (let d = dir; ; d = dirOf(d)) {
      const under = apps.filter(({ s }) => within(s.path, d))
      if (under.length > 0 || d === '') return under
    }
  }
  for (const path of paths) {
    const folders = path.split('/').slice(0, -1)
    if (folders.some((f) => FOREIGN.has(f.toLowerCase()))) continue
    const name = baseOf(path)
    const dir = dirOf(path)
    if (README.test(name)) {
      const own = services.findIndex((s) => s.path === dir)
      if (own >= 0) result[own]!.readme = true
      else if (holder(path) === null) {
        const under = below(dir)
        if (under.length === 1) result[under[0]!.i]!.readme = true
      }
      continue
    }
    if (DOCKERFILE.test(name)) {
      const own = holder(path)
      const under = below(dir).filter(({ s }) => s.path !== dir && within(s.path, dir) && (own === null || s.path !== services[own]!.path))
      if (own !== null) result[own]!.dockerfile = true
      else if (under.length === 1) result[under[0]!.i]!.dockerfile = true
      if (under.length > (own === null ? 1 : 0)) {
        const text = (await read(path)) ?? ''
        for (const { s, i } of under) if (new RegExp(`(^|[\\s/"'.])${escape(s.path.slice(dir ? dir.length + 1 : 0))}([\\s/"']|$)`, 'm').test(text)) result[i]!.dockerfile = true
      }
      continue
    }
    if (!languageOfFile(path)) continue
    const testDirs = folders.filter((f) => TEST_DIR.test(f))
    if (testDirs.length === 0 && !TEST_FILE.test(path)) continue
    const tested = testDirs.map(testedName).findLast((n) => n !== '') ?? ''
    const named = tested ? apps.find(({ s }) => baseOf(s.path).toLowerCase() === tested || s.name.toLowerCase() === tested) : undefined
    const i = named ? named.i : (holder(path) ?? (below(dir).length === 1 ? below(dir)[0]!.i : null))
    if (i !== null) result[i]!.tests = true
  }
  return result
}

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/**
 * One folder is one service. Where two build files claim it, the stronger
 * kind names it, and a web front end in it makes the whole a front end. A
 * front end and a .NET web host nested one in the other are one app, as with
 * a backend-for-frontend that serves its Angular shell. A Functions host or a
 * Dockerfile inside another service belongs to it, and one that holds other
 * services is only their shared settings.
 */
function merge(found: Found[]): Found[] {
  const rank = (f: Found) => MARKERS.indexOf(f.marker)
  const byPath = new Map<string, Found>()
  for (const f of [...found].sort((a, b) => rank(a) - rank(b))) {
    const held = byPath.get(f.path)
    if (!held) byPath.set(f.path, { ...f })
    else if (f.type === 'frontend') held.type = 'frontend'
  }
  const all = [...byPath.values()]
  const web = (f: Found) => (f.marker === 'node' && f.type === 'frontend') || (f.marker === 'dotnet' && (f.type === 'frontend' || f.type === 'backend'))
  return all
    .filter((f) => {
      const inner = all.filter((o) => o.path !== f.path && within(o.path, f.path))
      if ((f.marker === 'functions' || f.marker === 'docker') && inner.length > 0) return false
      const owner = all.find((o) => o.path !== f.path && within(f.path, o.path))
      if (!owner) return true
      if (web(f) && web(owner) && f.marker !== owner.marker && (f.type === 'frontend' || owner.type === 'frontend')) {
        owner.type = 'frontend'
        return false
      }
      return f.marker !== 'functions' && f.marker !== 'docker'
    })
    .sort((a, b) => a.path.localeCompare(b.path))
}

/** Whether any Terraform here is applied on its own: it keeps state in a backend, or has variables files to apply with. */
async function terraformRoot(files: string[], terraform: string[], read: Read): Promise<boolean> {
  if (terraform.length === 0) return false
  if (files.some((f) => /\.(tfvars|tfbackend)$/.test(f) || /\.tfvars\.json$/.test(f))) return true
  const likely = terraform.filter((f) => /^(backend|main|providers|versions|terraform)\.tf$/.test(baseOf(f))).slice(0, 40)
  for (const f of likely) if (/^\s*backend\s+"?[\w-]+"?\s*\{/m.test((await read(f)) ?? '')) return true
  return false
}

/** Reads at most `budget` files, then answers null, so one huge repository cannot hold a sweep up. */
export function budgeted(read: Read, budget = READ_BUDGET): { read: Read; spent: () => number } {
  let spent = 0
  return {
    read: async (path) => (++spent > budget ? null : read(path)),
    spent: () => spent,
  }
}

/**
 * The services at the head of a repository's default branch. The last answer
 * stands while the branch has not moved and the rules have not changed, so a
 * quiet repository costs one look at its head.
 */
export async function readServices(files: RepoFiles | null, repoName: string, previous: RepoServices | null | undefined): Promise<{ services: RepoServices; reused: boolean; read: number }> {
  if (!files) return { services: { commit: null, rules: SERVICE_RULES, services: [] }, reused: false, read: 0 }
  if (previous && previous.commit === files.commit && previous.rules === SERVICE_RULES) return { services: previous, reused: true, read: 0 }
  const reader = budgeted((p) => files.read(p))
  const found = await detectServices(await files.list(), reader.read, repoName)
  const services = await Promise.all(
    found.map(async (s) => {
      const last = await files.lastChange(s.path)
      return { ...s, changedAt: last?.at ?? null, changedBy: last?.by ?? null, changedByEmail: last?.email ?? null, humanChangedAt: last?.humanAt ?? null, humanChangedBy: last?.humanBy ?? null, humanChangedByEmail: last?.humanEmail ?? null }
    }),
  )
  return { services: { commit: files.commit, rules: SERVICE_RULES, services }, reused: false, read: reader.spent() }
}

