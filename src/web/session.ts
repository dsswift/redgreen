// What the web app does when a person's sign-in has ended while a page was open.

const KEY = 'redgreen.signin'
const AGAIN_AFTER_MS = 60_000

export const SIGNED_OUT = 'Your sign-in has ended. Reload the page to sign in again.'

/**
 * Whether an answer means the person is no longer signed in: a 401, or a
 * redirect, which is the gate in front sending them to sign in. A request must
 * be made with `redirect: 'manual'` for the redirect to be seen at all.
 */
export const signedOut = (response: { type: string; status: number }): boolean => response.type === 'opaqueredirect' || response.status === 401

export interface Page {
  storage: Pick<Storage, 'getItem' | 'setItem'>
  reload: () => void
  now: number
}

/**
 * Reloads the page, so the sign-in runs and comes back to where the person
 * was. False when a reload a moment ago did not help: the caller says so
 * instead of reloading for ever.
 */
export function signInAgain(page: Page): boolean {
  const last = Number(page.storage.getItem(KEY))
  if (last && page.now - last < AGAIN_AFTER_MS) return false
  page.storage.setItem(KEY, String(page.now))
  page.reload()
  return true
}
