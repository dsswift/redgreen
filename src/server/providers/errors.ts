/** True when `error` carries an HTTP status among `statuses`. Every provider client throws errors shaped this way. */
export function isStatus(error: unknown, ...statuses: number[]): boolean {
  return typeof error === 'object' && error !== null && 'status' in error && statuses.includes(Number(error.status))
}
