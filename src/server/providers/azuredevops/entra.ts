/** The application every Azure DevOps organization accepts tokens for. */
const AZURE_DEVOPS_RESOURCE = '499b84ac-1321-427f-aa17-267ca6975798'

/** An Entra application that has been added to an Azure DevOps organization as a user. */
export interface ServicePrincipal {
  tenantId: string
  clientId: string
  clientSecret: string
}

export interface AccessToken {
  accessToken: string
  /** ISO time the token stops working. Entra issues them for about an hour. */
  expiresAt: string
}

interface TokenResponse {
  access_token: string
  expires_in: number
}

/** Asks Entra for a token as the application itself. There is no refresh token; the next one is requested the same way. */
export async function requestToken(principal: ServicePrincipal): Promise<AccessToken> {
  const response = await fetch(`https://login.microsoftonline.com/${encodeURIComponent(principal.tenantId)}/oauth2/v2.0/token`, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded', accept: 'application/json', 'user-agent': 'redgreen' },
    body: new URLSearchParams({
      grant_type: 'client_credentials',
      client_id: principal.clientId,
      client_secret: principal.clientSecret,
      scope: `${AZURE_DEVOPS_RESOURCE}/.default`,
    }).toString(),
  })
  const text = await response.text()
  if (!response.ok) throw new Error(`Entra token request failed (${response.status}): ${describe(text)}`)
  const body = JSON.parse(text) as TokenResponse
  return { accessToken: body.access_token, expiresAt: new Date(Date.now() + body.expires_in * 1000).toISOString() }
}

/** Entra explains a refusal in `error_description`; what comes before its trace ids is the readable part. */
function describe(text: string): string {
  try {
    const body = JSON.parse(text) as { error_description?: string; error?: string }
    return (body.error_description ?? body.error ?? text).split(/\r?\n|\s*Trace ID:/)[0]?.slice(0, 300) ?? ''
  } catch {
    return text.slice(0, 200)
  }
}
