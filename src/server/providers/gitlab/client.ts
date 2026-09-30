import type { Logger } from '../../log.ts'
import type { Store } from '../../store.ts'
import type { RateLimits } from '../rate-limits.ts'

interface CacheEntry {
  data: unknown
  link: string | undefined
}

export type Query = Record<string, string | number | boolean | undefined>

/** Thrown for any non-2xx answer. `status` lets callers treat 403 and 404 as "feed not available". */
export class GitlabHttpError extends Error {
  readonly status: number
  readonly url: string

  constructor(status: number, url: string, body: string) {
    super(`GitLab ${status} for ${url}: ${body.slice(0, 200)}`)
    this.status = status
    this.url = url
  }
}

/** Headers that authenticate one request. Resolved per call so an OAuth token can be refreshed underneath. */
export type AuthHeaders = () => Promise<Record<string, string>>

/**
 * A thin GitLab REST and GraphQL client. Every GET is conditional: when a
 * response carried an ETag, the next request for the same URL sends it back
 * and a 304 is served from the cache without counting against the rate limit.
 * Cache keys are scoped per token so two connections never share a body.
 */
export class GitlabClient {
  readonly baseUrl: string
  private readonly auth: AuthHeaders
  private readonly scope: string
  private readonly store: Store
  private readonly limits: RateLimits
  private readonly log: Logger

  constructor(baseUrl: string, auth: AuthHeaders, scope: string, store: Store, limits: RateLimits, log: Logger) {
    this.baseUrl = baseUrl.replace(/\/$/, '')
    this.auth = auth
    this.scope = scope
    this.store = store
    this.limits = limits
    this.log = log
  }

  /** One page. `path` is relative to `/api/v4`. */
  async get<T>(path: string, query: Query = {}): Promise<T> {
    return (await this.page<T>(this.url(path, query))).data
  }

  /** Every page, following the `Link: rel="next"` header. */
  async paginate<T>(path: string, query: Query = {}): Promise<T[]> {
    const items: T[] = []
    let next: string | null = this.url(path, { per_page: 100, ...query })
    while (next) {
      const page: { data: T[]; link: string | undefined } = await this.page<T[]>(next)
      items.push(...page.data)
      next = nextLink(page.link)
    }
    return items
  }

  async graphql<T>(query: string, variables: Record<string, unknown>): Promise<{ data: T | null; errors: { message: string }[] }> {
    const url = `${this.baseUrl}/api/graphql`
    const response = await fetch(url, {
      method: 'POST',
      headers: { ...(await this.auth()), 'content-type': 'application/json', 'user-agent': 'redgreen' },
      body: JSON.stringify({ query, variables }),
    })
    this.recordLimits(response.headers)
    const text = await response.text()
    if (!response.ok) throw new GitlabHttpError(response.status, url, text)
    const body = JSON.parse(text) as { data?: T | null; errors?: { message: string }[] }
    this.log.debug('graphql', { url, errors: body.errors?.length ?? 0 })
    return { data: body.data ?? null, errors: body.errors ?? [] }
  }

  private url(path: string, query: Query): string {
    const url = new URL(`${this.baseUrl}/api/v4/${path.replace(/^\//, '')}`)
    for (const [key, value] of Object.entries(query)) if (value !== undefined) url.searchParams.set(key, String(value))
    return url.toString()
  }

  private async page<T>(url: string): Promise<{ data: T; link: string | undefined }> {
    const key = `${this.scope} ${url}`
    const cached = this.store.getCached(key)
    const headers: Record<string, string> = { ...(await this.auth()), accept: 'application/json', 'user-agent': 'redgreen' }
    if (cached) headers['if-none-match'] = cached.etag
    const response = await fetch(url, { headers })
    this.recordLimits(response.headers)
    if (response.status === 304 && cached) {
      this.store.touchCached(key, new Date().toISOString())
      const entry = JSON.parse(cached.body) as CacheEntry
      this.log.debug('served from etag cache', { url })
      return { data: entry.data as T, link: entry.link }
    }
    const text = await response.text()
    if (!response.ok) throw new GitlabHttpError(response.status, url, text)
    const data = JSON.parse(text) as T
    const etag = response.headers.get('etag')
    const link = response.headers.get('link') ?? undefined
    this.log.debug('fetched', { url, status: response.status, etag: Boolean(etag), conditional: Boolean(cached) })
    if (etag) {
      const entry: CacheEntry = { data, link }
      this.store.putCached(key, etag, JSON.stringify(entry), new Date().toISOString())
    }
    return { data, link }
  }

  private recordLimits(headers: Headers): void {
    this.limits.record(this.scope, headers.get('ratelimit-remaining'), headers.get('ratelimit-limit'), headers.get('ratelimit-reset'))
  }
}

/** The URL marked `rel="next"` in a Link header, or null on the last page. */
export function nextLink(link: string | undefined): string | null {
  if (!link) return null
  for (const part of link.split(',')) {
    const match = /<([^>]+)>\s*;\s*rel="next"/.exec(part)
    if (match?.[1]) return match[1]
  }
  return null
}

/** A project or group path as GitLab wants it in a URL. */
export function encodePath(path: string): string {
  return encodeURIComponent(path)
}
