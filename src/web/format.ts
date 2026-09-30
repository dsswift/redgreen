const UNITS: [number, string][] = [
  [365 * 86_400, 'y'],
  [30 * 86_400, 'mo'],
  [7 * 86_400, 'w'],
  [86_400, 'd'],
  [3_600, 'h'],
  [60, 'm'],
]

/** "41d", "3h", "just now". */
export function ago(iso: string | null, now = Date.now()): string {
  if (!iso) return 'never'
  const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  for (const [size, unit] of UNITS) if (seconds >= size) return `${Math.floor(seconds / size)}${unit}`
  return seconds < 10 ? 'just now' : `${seconds}s`
}

/** "41 days", "3 hours". */
export function duration(iso: string | null, now = Date.now()): string {
  if (!iso) return ''
  const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  const names: Record<string, string> = { y: 'year', mo: 'month', w: 'week', d: 'day', h: 'hour', m: 'minute' }
  for (const [size, unit] of UNITS) {
    if (seconds >= size) {
      const n = Math.floor(seconds / size)
      return `${n} ${names[unit]}${n === 1 ? '' : 's'}`
    }
  }
  return 'moments'
}

export function date(iso: string | null): string {
  return iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

export function percent(ratio: number | null): string {
  return ratio === null ? '' : `${Math.round(ratio * 100)}%`
}

export function compact(n: number): string {
  return new Intl.NumberFormat(undefined, { notation: 'compact' }).format(n)
}

export function untilMinutes(iso: string, now = Date.now()): number {
  return Math.max(0, Math.round((Date.parse(iso) - now) / 60_000))
}
