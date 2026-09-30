import type { Octokit } from 'octokit'
import type { Logger } from '../../log.ts'
import type { Store } from '../../store.ts'
import type { RateLimit } from '../provider.ts'

interface CacheEntry {
  data: unknown
  link: string | undefined
}

/** Rate-limit headers seen per token scope, for the settings page. */
export class RateLimits {
  private readonly seen = new Map<string, RateLimit>()

  record(scope: string, headers: Record<string, unknown>): void {
    const remaining = Number(headers['x-ratelimit-remaining'])
    const limit = Number(headers['x-ratelimit-limit'])
    const reset = Number(headers['x-ratelimit-reset'])
    if (!Number.isFinite(remaining) || !Number.isFinite(limit) || !Number.isFinite(reset)) return
    this.seen.set(scope, { scope, remaining, limit, resetAt: new Date(reset * 1000).toISOString() })
  }

  list(): RateLimit[] {
    return [...this.seen.values()].sort((a, b) => a.scope.localeCompare(b.scope))
  }
}

/**
 * Makes every GET conditional. GitHub answers an unchanged resource with 304
 * and does not count it against the rate limit, so a full sweep of a quiet
 * estate costs almost nothing. Cache keys are scoped per token so two
 * installations never share a body.
 */
export function withConditionalRequests(octokit: Octokit, scope: string, store: Store, limits: RateLimits, log: Logger): Octokit {
  octokit.hook.wrap('request', async (request, options) => {
    if (options.method !== 'GET') return request(options)
    const url = octokit.request.endpoint(options).url
    const key = `${scope} ${url}`
    const cached = store.getCached(key)
    // Hooks share one options object, so the header has to be set in place.
    if (cached) options.headers = { ...options.headers, 'if-none-match': cached.etag }
    try {
      const response = await request(options)
      limits.record(scope, response.headers)
      const etag = response.headers.etag
      log.debug('fetched', { url, status: response.status, etag: Boolean(etag), conditional: Boolean(cached) })
      if (etag) {
        const entry: CacheEntry = { data: response.data, link: response.headers.link }
        store.putCached(key, etag, JSON.stringify(entry), new Date().toISOString())
      }
      return response
    } catch (error) {
      if (cached && isStatus(error, 304)) {
        const notModified = error as { response?: { headers?: Record<string, unknown> } }
        if (notModified.response?.headers) limits.record(scope, notModified.response.headers)
        store.touchCached(key, new Date().toISOString())
        const entry = JSON.parse(cached.body) as CacheEntry
        log.debug('served from etag cache', { url })
        return { status: 200, url, headers: entry.link ? { link: entry.link } : {}, data: entry.data }
      }
      throw error
    }
  })
  return octokit
}

export function isStatus(error: unknown, ...statuses: number[]): boolean {
  return typeof error === 'object' && error !== null && 'status' in error && statuses.includes(Number(error.status))
}
