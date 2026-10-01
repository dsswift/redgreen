import type { Logger } from '../../log.ts'
import type { Store } from '../../store.ts'
import type { RateLimits } from '../rate-limits.ts'

interface CacheEntry {
  data: unknown
  continuation: string | undefined
}

export type Query = Record<string, string | number | boolean | undefined>

/** REST version asked for unless a call names its own. Preview resources need theirs spelled out. */
const API_VERSION = '7.1'

/** Thrown for any answer that is not usable JSON. `status` lets callers treat 403 and 404 as "feed not available". */
export class AzureDevopsHttpError extends Error {
  readonly status: number
  readonly url: string

  constructor(status: number, url: string, body: string) {
    super(`Azure DevOps ${status} for ${url}: ${body.slice(0, 200)}`)
    this.status = status
    this.url = url
  }
}

/** Headers that authenticate one request. Resolved per call so the token can be renewed underneath. */
export type AuthHeaders = () => Promise<Record<string, string>>

/**
 * A thin Azure DevOps REST client. Every GET is conditional: when a response
 * carried an ETag, the next request for the same URL sends it back and a 304
 * is served from the cache. Azure DevOps sends an ETag on few resources, so
 * most requests are answered in full and count against the organization's
 * throttle. Cache keys are scoped per credential so two never share a body.
 */
export class AzureDevopsClient {
  private readonly auth: AuthHeaders
  private readonly scope: string
  private readonly store: Store
  private readonly limits: RateLimits
  private readonly log: Logger

  constructor(auth: AuthHeaders, scope: string, store: Store, limits: RateLimits, log: Logger) {
    this.auth = auth
    this.scope = scope
    this.store = store
    this.limits = limits
    this.log = log
  }

  /** One response. `url` is absolute; the services live on more than one host. */
  async get<T>(url: string, query: Query = {}): Promise<T> {
    return (await this.page<T>(withQuery(url, query))).data
  }

  /** Every item of a `{ value: [...] }` collection, following the continuation token header. */
  async list<T>(url: string, query: Query = {}): Promise<T[]> {
    const items: T[] = []
    let continuation: string | undefined
    do {
      const page: { data: T[] | { value?: T[] }; continuation: string | undefined } = await this.page(withQuery(url, { ...query, continuationToken: continuation }))
      items.push(...(Array.isArray(page.data) ? page.data : (page.data.value ?? [])))
      continuation = page.continuation
    } while (continuation)
    return items
  }

  private async page<T>(url: string): Promise<{ data: T; continuation: string | undefined }> {
    const key = `${this.scope} ${url}`
    const cached = this.store.getCached(key)
    const headers: Record<string, string> = { ...(await this.auth()), accept: 'application/json', 'user-agent': 'redgreen' }
    if (cached) headers['if-none-match'] = cached.etag
    // A token the organization does not accept is answered with a redirect to a sign-in page, not a 401.
    const response = await fetch(url, { headers, redirect: 'manual' })
    this.limits.record(this.scope, response.headers.get('x-ratelimit-remaining'), response.headers.get('x-ratelimit-limit'), response.headers.get('x-ratelimit-reset'))
    if (response.status === 304 && cached) {
      this.store.touchCached(key, new Date().toISOString())
      const entry = JSON.parse(cached.body) as CacheEntry
      this.log.debug('served from etag cache', { url })
      return { data: entry.data as T, continuation: entry.continuation }
    }
    if (response.status === 203 || (response.status >= 300 && response.status < 400)) {
      throw new AzureDevopsHttpError(401, url, 'the organization asked for a sign-in; is the application a member of it?')
    }
    const text = await response.text()
    if (!response.ok) throw new AzureDevopsHttpError(response.status, url, text)
    const data = JSON.parse(text) as T
    const etag = response.headers.get('etag')
    const continuation = response.headers.get('x-ms-continuationtoken') ?? undefined
    this.log.debug('fetched', { url, status: response.status, etag: Boolean(etag), conditional: Boolean(cached) })
    if (etag) {
      const entry: CacheEntry = { data, continuation }
      this.store.putCached(key, etag, JSON.stringify(entry), new Date().toISOString())
    }
    return { data, continuation }
  }
}

function withQuery(url: string, query: Query): string {
  const built = new URL(url)
  for (const [key, value] of Object.entries({ 'api-version': API_VERSION, ...query })) if (value !== undefined) built.searchParams.set(key, String(value))
  return built.toString()
}
