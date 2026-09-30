import type { Octokit } from 'octokit'
import type { Logger } from '../../log.ts'
import type { Store } from '../../store.ts'
import { isStatus } from '../errors.ts'
import type { RateLimits } from '../rate-limits.ts'

interface CacheEntry {
  data: unknown
  link: string | undefined
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
      limits.record(scope, response.headers['x-ratelimit-remaining'], response.headers['x-ratelimit-limit'], response.headers['x-ratelimit-reset'])
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
        const h = notModified.response?.headers
        if (h) limits.record(scope, h['x-ratelimit-remaining'], h['x-ratelimit-limit'], h['x-ratelimit-reset'])
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
