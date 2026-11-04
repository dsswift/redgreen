// Describes redgreen itself to the Orrery it runs beside, from the
// orrery.yaml at the repository root: its logo, put as a custom icon and
// worn by its service, the service it is, the fields and
// relations that place it in the estate's own scheme, the APIs it provides,
// and the APIs it calls. On when ORRERY_URL and
// ORRERY_PUSH_TOKEN are both set. The service is patched, so the fields other
// sources write stay theirs, and no snapshot is opened, so nothing another
// source wrote is removed. It writes again after a failure with a growing
// wait, and every six hours so the catalog keeps seeing it.

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { parse } from 'yaml'
import { z } from 'zod'
import type { Config } from './config.ts'
import { ICON_NAME, iconFile, iconType, type IconFile } from './icons.ts'
import type { Logger } from './log.ts'
import type { Telemetry } from './telemetry.ts'

/** Where the description lives: the repository root, which is the image's working directory too. */
export const DESCRIPTOR_PATH = resolve(import.meta.dirname, '../../orrery.yaml')

const API_KINDS = ['rest', 'graphql', 'grpc', 'event', 'web'] as const

const providedApi = z.strictObject({
  id: z.string(),
  title: z.string(),
  kind: z.string(),
  description: z.string().default(''),
})

const NAME = /^[a-z][a-z0-9_]*$/

const descriptor = z.strictObject({
  service: z.string().default(''),
  /** The app's logo: a custom icon name and the file, from the repository root, that holds it. */
  icon: z.strictObject({ name: z.string().regex(ICON_NAME), file: z.string().min(1) }).nullish().transform((v) => v ?? null),
  /** Fields and relations that place the service in one estate's own scheme. The estate defines them; left out, they are never written. */
  fields: z.record(z.string().regex(NAME), z.unknown()).nullish().transform((v) => v ?? {}),
  relations: z.record(z.string().regex(NAME), z.union([z.string().min(1), z.array(z.string().min(1))])).nullish().transform((v) => v ?? {}),
  provides: z.array(providedApi).nullish().transform((v) => v ?? []),
  consumes: z.array(z.string()).nullish().transform((v) => v ?? []),
  will_consume: z.array(z.string()).nullish().transform((v) => v ?? []),
})

export type Descriptor = z.output<typeof descriptor>

/** A description with the bytes of the logo it names, read from beside the file. */
export type Described = Descriptor & { image?: IconFile }

/** Reads an orrery.yaml and refuses one the hub would refuse. */
export function parseDescriptor(text: string): Descriptor {
  const parsed = descriptor.safeParse(parse(text) ?? {})
  if (!parsed.success) throw new Error(`orrery.yaml: ${z.prettifyError(parsed.error)}`)
  const d = parsed.data
  if (!d.service) throw new Error('orrery.yaml: service is required')
  if (d.icon && !iconType(d.icon.file)) throw new Error(`orrery.yaml: icon file "${d.icon.file}" is not .svg, .png, or .webp`)
  const seen = new Set<string>()
  d.provides.forEach((a, i) => {
    if (!a.id || !a.title) throw new Error(`orrery.yaml: provides[${i}] needs an id and a title`)
    if (!(API_KINDS as readonly string[]).includes(a.kind)) throw new Error(`orrery.yaml: provides[${i}] kind "${a.kind}" is not rest, graphql, grpc, event, or web`)
    if (seen.has(a.id)) throw new Error(`orrery.yaml: api "${a.id}" is listed twice`)
    seen.add(a.id)
  })
  return d
}

export interface DescribeWrite {
  method: 'PATCH' | 'PUT'
  blueprint: 'sw_service' | 'sw_api'
  key: string
  body: { title?: string; icon?: string; fields: Record<string, unknown>; relations: Record<string, unknown> }
}

/**
 * The writes that describe the app: its service, patched and wearing its
 * logo when it names one, then one api per provided API. An empty list is written as an empty list, so a connector
 * removed from the file is removed from the catalog.
 */
export function describeWrites(d: Descriptor): DescribeWrite[] {
  return [
    {
      method: 'PATCH',
      blueprint: 'sw_service',
      key: d.service,
      body: { ...(d.icon ? { icon: d.icon.name } : {}), fields: { ...d.fields, origin: 'built' }, relations: { ...d.relations, consumes: d.consumes, will_consume: d.will_consume } },
    },
    ...d.provides.map((a): DescribeWrite => ({
      method: 'PUT',
      blueprint: 'sw_api',
      key: `${d.service}/${a.id}`,
      body: { title: a.title, fields: { kind: a.kind, description: a.description, visibility: 'internal', external: false }, relations: { service: d.service } },
    })),
  ]
}

/**
 * The blueprints a description writes, named as the software pack of the
 * blueprint standard names them, offered so a hub without them gains them.
 * `formerly` takes over a hub's older `service` and `api`.
 */
export const DESCRIBE_BLUEPRINTS = [
  {
    id: 'sw_service',
    title: 'Service',
    group: 'Software',
    formerly: ['service'],
    description: 'One deployable piece of software, keyed by its repository path. Built here or adopted open source.',
    fields: { origin: { type: 'enum', values: ['built', 'adopted'], chip: true, description: 'Built here, or open source the estate runs.' } },
    relations: {
      consumes: { target: 'sw_api', many: true, title: 'Consumes', inverseTitle: 'Consumed by' },
      will_consume: { target: 'sw_api', many: true, title: 'Will consume', inverseTitle: 'Wanted by' },
    },
  },
  {
    id: 'sw_api',
    title: 'API',
    group: 'Software',
    formerly: ['api'],
    description: 'An interface a service offers others, keyed <service key>/<id>.',
    fields: {
      description: { type: 'string' },
      kind: { type: 'enum', values: ['rest', 'graphql', 'grpc', 'event', 'web'] },
      visibility: { type: 'enum', values: ['internal', 'partner', 'public'] },
      external: { type: 'boolean' },
    },
    relations: { service: { target: 'sw_service', title: 'Provided by', inverseTitle: 'Provides' } },
  },
]

const EVERY_MS = 6 * 60 * 60_000
const BACKOFF_MS = [30_000, 60_000, 5 * 60_000, 15 * 60_000]
const TIMEOUT_MS = 60_000

export interface DescriberOptions {
  fetch?: typeof fetch
  everyMs?: number
  backoffMs?: number[]
}

/** Keeps redgreen described in Orrery for as long as it runs. */
export class Describer {
  private readonly url: string
  private readonly token: string
  private readonly desc: Described
  private readonly telemetry: Pick<Telemetry, 'described'>
  private readonly log: Logger
  private readonly fetch: typeof fetch
  private readonly everyMs: number
  private readonly backoffMs: number[]
  private readonly abort = new AbortController()
  private timer: NodeJS.Timeout | null = null
  private failures = 0

  constructor(config: Pick<Config, 'ORRERY_URL' | 'ORRERY_PUSH_TOKEN'>, desc: Described, telemetry: Pick<Telemetry, 'described'>, log: Logger, options: DescriberOptions = {}) {
    this.url = (config.ORRERY_URL ?? '').replace(/\/$/, '')
    this.token = config.ORRERY_PUSH_TOKEN ?? ''
    this.desc = desc
    this.telemetry = telemetry
    this.log = log
    this.fetch = options.fetch ?? fetch
    this.everyMs = options.everyMs ?? EVERY_MS
    this.backoffMs = options.backoffMs ?? BACKOFF_MS
  }

  enabled(): boolean {
    return !!this.url && !!this.token
  }

  start(): void {
    if (!this.enabled()) {
      this.log.info('orrery describe off: ORRERY_URL and ORRERY_PUSH_TOKEN are not both set')
      return
    }
    this.log.info('orrery describe on', { url: this.url, service: this.desc.service })
    void this.tick()
  }

  stop(): void {
    if (this.timer) clearTimeout(this.timer)
    this.timer = null
    this.abort.abort()
  }

  private async tick(): Promise<void> {
    const ok = await this.once()
    if (this.abort.signal.aborted) return
    let wait = this.everyMs
    if (ok) this.failures = 0
    else wait = this.backoffMs[Math.min(this.failures++, this.backoffMs.length - 1)] ?? this.everyMs
    this.timer = setTimeout(() => void this.tick(), wait)
    this.timer.unref()
  }

  /** Writes the description once and says whether it took. */
  async once(): Promise<boolean> {
    try {
      await this.describe()
      this.log.info('orrery described', { service: this.desc.service, icon: this.desc.icon?.name ?? null, apis: this.desc.provides.length, consumes: this.desc.consumes.length, will_consume: this.desc.will_consume.length })
      this.telemetry.described('ok')
      return true
    } catch (error) {
      if (this.abort.signal.aborted) return false
      this.log.warn('orrery describe failed', { service: this.desc.service, error })
      this.telemetry.described('failed')
      return false
    }
  }

  private async describe(): Promise<void> {
    const offered = (await this.call('PUT', '/ingest/blueprints', { blueprints: DESCRIBE_BLUEPRINTS })) as { created?: string[]; extended?: string[]; rejected?: { blueprint: string; reason: string }[] } | null
    const refused = offered?.rejected ?? []
    if (refused.length) throw new Error(`orrery: ${refused.length} blueprint(s) refused, first: ${refused[0]?.blueprint}: ${refused[0]?.reason}`)
    // The logo goes first, so the service never names an icon the hub lacks. The same bytes again change nothing there.
    if (this.desc.icon) {
      const image = this.desc.image
      if (!image) throw new Error(`orrery.yaml: icon file "${this.desc.icon.file}" was not read`)
      await this.send('PUT', `/api/icons/${encodeURIComponent(image.name)}`, image.type, Buffer.from(image.data))
    }
    for (const w of describeWrites(this.desc)) {
      await this.call(w.method, `/ingest/entities/${encodeURIComponent(w.blueprint)}/${encodeURIComponent(w.key)}`, w.body)
    }
  }

  private call(method: string, path: string, body: unknown): Promise<unknown> {
    return this.send(method, path, 'application/json', JSON.stringify(body))
  }

  private async send(method: string, path: string, type: string, body: BodyInit): Promise<unknown> {
    const signal = AbortSignal.any([this.abort.signal, AbortSignal.timeout(TIMEOUT_MS)])
    const res = await this.fetch(this.url + path, { method, headers: { authorization: `Bearer ${this.token}`, 'content-type': type, accept: 'application/json' }, body, signal })
    const text = await res.text()
    if (!res.ok) throw new Error(`orrery ${method} ${path}: ${res.status} ${text.slice(0, 300)}`)
    return text ? JSON.parse(text) : null
  }
}

/** Reads the description beside the app, and the logo it names from the same folder. */
export function loadDescriptor(path = DESCRIPTOR_PATH): Described {
  const d = parseDescriptor(readFileSync(path, 'utf8'))
  if (!d.icon) return d
  return { ...d, image: iconFile(d.icon, readFileSync(resolve(dirname(path), d.icon.file))) }
}
