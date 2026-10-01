import { z } from 'zod'

const csv = z
  .string()
  .default('')
  .transform((s) => s.split(',').map((v) => v.trim()).filter(Boolean))

const schema = z.object({
  PORT: z.coerce.number().int().positive().default(8080),
  HOST: z.string().default('0.0.0.0'),
  DATA_DIR: z.string().default('./data'),
  /** The URL a browser and GitHub reach this instance on. Drives the app manifest and webhook URL. */
  PUBLIC_URL: z.string().url().default('http://localhost:8080'),
  /** Minutes between full sweeps of every repo. Webhooks refresh single repos in between. */
  SYNC_INTERVAL_MINUTES: z.coerce.number().positive().default(5),
  /** Repos synced at the same time. */
  SYNC_CONCURRENCY: z.coerce.number().int().positive().default(4),
  /** An Orrery hub's ingest URL. With a token, every repo is pushed to it as a `service` entity. */
  ORRERY_URL: z.string().url().optional(),
  /** A source token for the hub. */
  ORRERY_TOKEN: z.string().optional(),
  /** Base64 32-byte key that encrypts provider credentials at rest. Generated into DATA_DIR when unset. */
  SECRET_KEY: z.string().optional(),

  /** Logins allowed to sync without approval in the UI. Anyone else who installs the app waits as pending. */
  GITHUB_ALLOWED_ACCOUNTS: csv,
  /** GitHub App credentials from the environment win over the ones created through the setup flow. */
  GITHUB_APP_ID: z.string().optional(),
  GITHUB_APP_PRIVATE_KEY: z.string().optional(),
  GITHUB_APP_WEBHOOK_SECRET: z.string().optional(),
  GITHUB_APP_SLUG: z.string().optional(),
  /** A personal access token instead of an app. Meant for running locally; no webhooks, one shared rate limit. */
  GITHUB_TOKEN: z.string().optional(),
  /** Accounts the token syncs. Required with GITHUB_TOKEN. */
  GITHUB_TOKEN_ACCOUNTS: csv,

  /** Base URL of the GitLab instance. */
  GITLAB_URL: z.string().url().default('https://gitlab.com'),
  /** Group or user paths allowed to sync without approval in the UI. Every other namespace found waits as pending. */
  GITLAB_ALLOWED_ACCOUNTS: csv,
  /** OAuth application credentials from the environment win over the ones entered on the settings page. Authorizing still happens in the browser. */
  GITLAB_CLIENT_ID: z.string().optional(),
  GITLAB_CLIENT_SECRET: z.string().optional(),
  /** Secret token expected on webhooks GitLab sends. Generated when unset. */
  GITLAB_WEBHOOK_SECRET: z.string().optional(),
  /** A personal or group access token instead of OAuth. Meant for running locally. */
  GITLAB_TOKEN: z.string().optional(),
  /** Group or user paths the token syncs. Required with GITLAB_TOKEN. */
  GITLAB_TOKEN_ACCOUNTS: csv,
})

export type Config = z.infer<typeof schema>

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const parsed = schema.safeParse(env)
  if (!parsed.success) {
    throw new Error(`Invalid configuration: ${parsed.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`).join('; ')}`)
  }
  return parsed.data
}
