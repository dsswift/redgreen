// Finds the services a repository holds from the files that build them: a
// .NET project that is an app, a package.json that starts one, a Go main
// package, a Functions host, a Dockerfile, a Terraform or Bicep root. Every
// forge hands over the same two things, the file list at a commit and a way
// to read one file, so the rules here are the only place a service is
// recognised. The answer depends on nothing but the files, so the same
// commit always gives the same services.

import { languageOfFile, mainLanguage, type Language } from '../shared/languages.ts'
import type { DetectedService, RepoServices, ServiceType } from '../shared/model.ts'

/** Bump on any change to the rules below, so every repository is read again instead of trusting its last answer. */
export const SERVICE_RULES = 1

/** A repository's files at the head of its default branch. */
export interface RepoFiles {
  commit: string
  /** Every file path, relative to the root, `/` separated. */
  list(): Promise<string[]>
  /** One file's text, or null when it cannot be read. */
  read(path: string): Promise<string | null>
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

/** Every service in one repository's files. `repoName` names a service that sits at the root and names nothing itself. */
export async function detectServices(paths: string[], read: Read, repoName: string): Promise<DetectedService[]> {
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
  const services = await detectServices(await files.list(), reader.read, repoName)
  return { services: { commit: files.commit, rules: SERVICE_RULES, services }, reused: false, read: reader.spent() }
}

