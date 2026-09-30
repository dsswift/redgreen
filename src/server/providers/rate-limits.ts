import type { RateLimit } from '../../shared/api.ts'

/** Rate-limit headers seen per token scope, for the settings page. */
export class RateLimits {
  private readonly seen = new Map<string, RateLimit>()

  /** Records one response's counters. `reset` is seconds since the epoch. Ignores responses that carry none. */
  record(scope: string, remaining: unknown, limit: unknown, reset: unknown): void {
    const r = Number(remaining)
    const l = Number(limit)
    const t = Number(reset)
    if (remaining == null || limit == null || reset == null || !Number.isFinite(r) || !Number.isFinite(l) || !Number.isFinite(t)) return
    this.seen.set(scope, { scope, remaining: r, limit: l, resetAt: new Date(t * 1000).toISOString() })
  }

  list(): RateLimit[] {
    return [...this.seen.values()].sort((a, b) => a.scope.localeCompare(b.scope))
  }
}
