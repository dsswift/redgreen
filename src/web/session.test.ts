import { describe, expect, it } from 'vitest'
import { signInAgain, signedOut, type Page } from './session.ts'

function page(now: number, kept = new Map<string, string>()) {
  let reloads = 0
  const at: Page = { now, reload: () => void reloads++, storage: { getItem: (k) => kept.get(k) ?? null, setItem: (k, v) => void kept.set(k, v) } }
  return { at, kept, reloads: () => reloads }
}

describe('a sign-in that ended', () => {
  it('is read from a redirect or a 401 and from nothing else', () => {
    expect(signedOut({ type: 'opaqueredirect', status: 0 })).toBe(true)
    expect(signedOut({ type: 'basic', status: 401 })).toBe(true)
    expect(signedOut({ type: 'basic', status: 403 })).toBe(false)
    expect(signedOut({ type: 'basic', status: 500 })).toBe(false)
    expect(signedOut({ type: 'basic', status: 200 })).toBe(false)
  })

  it('reloads the page once, and not again when that did not help', () => {
    const first = page(1_000_000)
    expect(signInAgain(first.at)).toBe(true)
    expect(first.reloads()).toBe(1)

    const stillOut = page(1_005_000, first.kept)
    expect(signInAgain(stillOut.at)).toBe(false)
    expect(stillOut.reloads()).toBe(0)
  })

  it('reloads again when the last reload was long ago', () => {
    const first = page(1_000_000)
    signInAgain(first.at)
    const later = page(1_000_000 + 3_600_000, first.kept)
    expect(signInAgain(later.at)).toBe(true)
    expect(later.reloads()).toBe(1)
  })
})
