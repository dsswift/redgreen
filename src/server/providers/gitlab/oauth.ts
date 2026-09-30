import { randomBytes } from 'node:crypto'

/** The tokens GitLab hands back. Access tokens live two hours; the refresh token is single-use. */
export interface TokenSet {
  accessToken: string
  refreshToken: string
  /** ISO time the access token stops working. */
  expiresAt: string
}

interface TokenResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  created_at: number
}

/** Scope that reads every API redgreen needs, including GraphQL, and nothing that writes. */
export const SCOPE = 'read_api'

export function authorizeUrl(baseUrl: string, clientId: string, redirectUri: string, state: string): string {
  const url = new URL('/oauth/authorize', baseUrl)
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('redirect_uri', redirectUri)
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('state', state)
  url.searchParams.set('scope', SCOPE)
  return url.toString()
}

export function newState(): string {
  return randomBytes(16).toString('hex')
}

export async function exchangeCode(baseUrl: string, app: { clientId: string; clientSecret: string }, redirectUri: string, code: string): Promise<TokenSet> {
  return token(baseUrl, { grant_type: 'authorization_code', code, client_id: app.clientId, client_secret: app.clientSecret, redirect_uri: redirectUri })
}

export async function refresh(baseUrl: string, app: { clientId: string; clientSecret: string }, redirectUri: string, refreshToken: string): Promise<TokenSet> {
  return token(baseUrl, {
    grant_type: 'refresh_token',
    refresh_token: refreshToken,
    client_id: app.clientId,
    client_secret: app.clientSecret,
    redirect_uri: redirectUri,
  })
}

async function token(baseUrl: string, params: Record<string, string>): Promise<TokenSet> {
  const response = await fetch(new URL('/oauth/token', baseUrl), {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded', accept: 'application/json', 'user-agent': 'redgreen' },
    body: new URLSearchParams(params).toString(),
  })
  const text = await response.text()
  if (!response.ok) throw new Error(`GitLab token request failed (${response.status}): ${text.slice(0, 200)}`)
  const body = JSON.parse(text) as TokenResponse
  const issuedAt = body.created_at ? body.created_at * 1000 : Date.now()
  return { accessToken: body.access_token, refreshToken: body.refresh_token, expiresAt: new Date(issuedAt + body.expires_in * 1000).toISOString() }
}
