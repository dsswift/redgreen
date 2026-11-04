import { describe, expect, it } from 'vitest'
import { DESCRIBE_BLUEPRINTS, Describer, loadDescriptor, parseDescriptor } from './describe.ts'
import { iconFile } from './icons.ts'
import type { Logger } from './log.ts'

const silent: Logger = { debug() {}, info() {}, warn() {}, error() {}, child: () => silent }

const full = `
service: github/example/app
fields: {category: Things}
relations: {pillars: [platform/one]}
provides:
- id: rest
  title: App API
  kind: rest
  description: What it answers.
consumes: [github/example/other/api]
`

describe('parseDescriptor', () => {
  it('reads the orrery.yaml the image carries', () => {
    const d = loadDescriptor()
    expect(d.service).toBe('github/dsswift/redgreen')
    expect(d.relations).toEqual({ pillars: ['platform/outcomes', 'platform/catalog'] })
    expect(d.fields).toEqual({ category: 'Delivery outcomes' })
    expect(d.icon).toEqual({ name: 'redgreen', file: 'logo.svg' })
    expect(d.image).toMatchObject({ name: 'redgreen', type: 'image/svg+xml' })
  })

  it('refuses an icon the hub would refuse', () => {
    expect(() => parseDescriptor('service: s\nicon: {name: Bad-Name, file: logo.svg}\n')).toThrow(/icon/)
    expect(() => parseDescriptor('service: s\nicon: {name: ok, file: logo.gif}\n')).toThrow('not .svg, .png, or .webp')
    expect(() => parseDescriptor('service: s\nicon: {name: ok}\n')).toThrow(/file/)
  })

  it('refuses a file without a service', () => {
    expect(() => parseDescriptor('fields: {category: x}\n')).toThrow('service is required')
  })

  it('refuses an unknown key', () => {
    expect(() => parseDescriptor('service: s\nservices: []\n')).toThrow(/services/)
    expect(() => parseDescriptor('service: s\nprovides:\n- {id: a, title: A, kind: rest, url: x}\n')).toThrow(/url/)
  })

  it('refuses a field or relation name the hub would not take', () => {
    expect(() => parseDescriptor('service: s\nfields: {Category: x}\n')).toThrow(/Category|fields/)
    expect(() => parseDescriptor('service: s\nrelations: {pillars: [""]}\n')).toThrow(/relations/)
  })

  it('refuses a kind outside the list', () => {
    expect(() => parseDescriptor('service: s\nprovides:\n- {id: a, title: A, kind: soap}\n')).toThrow('kind "soap"')
  })

  it('refuses an api listed twice', () => {
    expect(() => parseDescriptor('service: s\nprovides:\n- {id: a, title: A, kind: rest}\n- {id: a, title: B, kind: web}\n')).toThrow('api "a" is listed twice')
  })
})

describe('Describer', () => {
  const capture = (status = 200) => {
    const calls: { method: string; url: string; auth: string; body: unknown }[] = []
    const fake = (async (url: string | URL | Request, init?: RequestInit) => {
      const type = new Headers(init?.headers).get('content-type')
      calls.push({ method: init?.method ?? 'GET', url: String(url), auth: new Headers(init?.headers).get('authorization') ?? '', body: type === 'application/json' ? JSON.parse(String(init?.body)) : { type, bytes: (init?.body as Uint8Array).byteLength } })
      return new Response(status === 200 ? '{}' : 'nope', { status })
    }) as typeof fetch
    return { calls, fake }
  }

  it('offers its blueprints, patches the service, and puts each api', async () => {
    const { calls, fake } = capture()
    const outcomes: string[] = []
    const r = new Describer({ ORRERY_URL: 'https://hub/', ORRERY_PUSH_TOKEN: 'push' }, parseDescriptor(full), { described: (o) => outcomes.push(o) }, silent, { fetch: fake })
    expect(await r.once()).toBe(true)
    expect(outcomes).toEqual(['ok'])
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'PUT https://hub/v1/ingest/blueprints',
      'PATCH https://hub/v1/ingest/entities/sw_service/github%2Fexample%2Fapp',
      'PUT https://hub/v1/ingest/entities/sw_api/github%2Fexample%2Fapp%2Frest',
    ])
    expect(calls.every((c) => c.auth === 'Bearer push')).toBe(true)
    expect(calls[0]?.body).toEqual({ blueprints: DESCRIBE_BLUEPRINTS })
    expect(calls[1]?.body).toEqual({ fields: { category: 'Things', origin: 'built' }, relations: { pillars: ['platform/one'], consumes: ['github/example/other/api'], will_consume: [] } })
    expect(calls[2]?.body).toEqual({ title: 'App API', fields: { kind: 'rest', description: 'What it answers.', visibility: 'internal', external: false }, relations: { service: 'github/example/app' } })
  })

  it('puts its logo before the service that wears it', async () => {
    const { calls, fake } = capture()
    const svg = new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg"/>')
    const d = parseDescriptor('service: github/example/app\nicon: {name: app, file: logo.svg}\n')
    const r = new Describer({ ORRERY_URL: 'https://hub', ORRERY_PUSH_TOKEN: 'push' }, { ...d, image: iconFile(d.icon!, svg) }, { described() {} }, silent, { fetch: fake })
    expect(await r.once()).toBe(true)
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual(['PUT https://hub/v1/ingest/blueprints', 'PUT https://hub/v1/icons/app', 'PATCH https://hub/v1/ingest/entities/sw_service/github%2Fexample%2Fapp'])
    expect(calls[1]?.body).toEqual({ type: 'image/svg+xml', bytes: svg.byteLength })
    expect(calls[2]?.body).toMatchObject({ icon: 'app' })
  })

  it('fails the describe when the logo it names was not read', async () => {
    const { calls, fake } = capture()
    const r = new Describer({ ORRERY_URL: 'https://hub', ORRERY_PUSH_TOKEN: 'push' }, parseDescriptor('service: s\nicon: {name: app, file: logo.svg}\n'), { described() {} }, silent, { fetch: fake })
    expect(await r.once()).toBe(false)
    expect(calls).toHaveLength(1)
  })

  it('writes only its origin and connectors when the file places it nowhere', async () => {
    const { calls, fake } = capture()
    const r = new Describer({ ORRERY_URL: 'https://hub', ORRERY_PUSH_TOKEN: 'push' }, parseDescriptor('service: github/example/app\n'), { described() {} }, silent, { fetch: fake })
    expect(await r.once()).toBe(true)
    expect(calls[1]?.body).toEqual({ fields: { origin: 'built' }, relations: { consumes: [], will_consume: [] } })
  })

  it('offers the software blueprints under their standard ids, taking over the old ones', () => {
    expect(DESCRIBE_BLUEPRINTS.map((b) => [b.id, b.formerly])).toEqual([
      ['sw_service', ['service']],
      ['sw_api', ['api']],
    ])
  })

  it('counts a failed write and stops at the first refusal', async () => {
    const { calls, fake } = capture(500)
    const outcomes: string[] = []
    const r = new Describer({ ORRERY_URL: 'https://hub', ORRERY_PUSH_TOKEN: 'push' }, parseDescriptor(full), { described: (o) => outcomes.push(o) }, silent, { fetch: fake })
    expect(await r.once()).toBe(false)
    expect(outcomes).toEqual(['failed'])
    expect(calls).toHaveLength(1)
  })

  it('is off without the push token', () => {
    const r = new Describer({ ORRERY_URL: 'https://hub' }, parseDescriptor(full), { described() {} }, silent)
    expect(r.enabled()).toBe(false)
  })
})
