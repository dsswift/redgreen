import { useState } from 'react'
import type { GitlabSetup, ProviderStatus, SettingsResponse } from '../../shared/api.ts'
import { PROVIDER_LABELS, type Account } from '../../shared/model.ts'
import { RULES, RULE_KEYS, type RuleSeverities } from '../../shared/rules.ts'
import { fetchManifest, useSaveGitlabApp, useSettings, useUpdateAccount, useUpdateRules } from '../api.ts'
import { untilMinutes } from '../format.ts'
import { Button, Empty, External, Kicker, Panel, SeveritySelect } from './ui.tsx'

const FIELD = 'rounded-sm border border-ink-600 bg-ink-850 px-3 py-1.5 font-mono text-xs text-ink-100 focus:border-ink-300 focus:outline-none'
const FIELD_LABEL = 'font-mono text-[10px] uppercase tracking-[0.18em] text-ink-400'

export function Settings() {
  const settings = useSettings()
  if (settings.isPending) return <p className="py-16 text-center font-mono text-xs text-ink-400">Loading…</p>
  if (settings.isError) return <Empty>{settings.error.message}</Empty>
  const data = settings.data
  const justCreated = new URLSearchParams(location.search).get('created')

  return (
    <div className="flex flex-col gap-10 pt-8">
      <div>
        <Kicker>Settings</Kicker>
        <h1 className="mt-2 text-3xl font-semibold tracking-tight">Providers, accounts, and rules</h1>
      </div>

      <section className="grid gap-4 md:grid-cols-2">
        {data.providers.map((p) => (
          <ProviderPanel key={p.kind} provider={p} settings={data} justCreated={justCreated === p.kind} />
        ))}
        <Panel className="flex flex-col justify-center p-5 opacity-60">
          <Kicker>Next</Kicker>
          <p className="mt-2 text-sm text-ink-300">Azure DevOps plugs in here. Same board, same rules; only the provider is new.</p>
        </Panel>
      </section>

      <section>
        <Kicker>Accounts</Kicker>
        {data.accounts.length === 0 ? (
          <p className="mt-3 text-sm text-ink-400">None yet. Install the GitHub App on an account, or authorize GitLab, and they appear here.</p>
        ) : (
          <ul className="mt-3 divide-y divide-ink-800 border-y border-ink-700">
            {data.accounts.map((a) => (
              <AccountRow key={a.id} account={a} />
            ))}
          </ul>
        )}
      </section>

      <Rules rules={data.rules} defaults={data.defaults} />
    </div>
  )
}

function ProviderPanel({ provider, settings, justCreated }: { provider: ProviderStatus; settings: SettingsResponse; justCreated: boolean }) {
  return (
    <Panel className="p-5">
      <div className="flex items-baseline justify-between">
        <h2 className="font-medium">{PROVIDER_LABELS[provider.kind]}</h2>
        <span className={`font-mono text-[11px] ${provider.configured ? 'text-green' : 'text-amber'}`}>
          {provider.configured ? `connected · ${provider.source}` : 'not connected'}
        </span>
      </div>
      {provider.configured ? (
        <div className="mt-3 flex flex-col gap-3 text-sm">
          {justCreated && provider.kind === 'github' && (
            <p className="rounded-sm border border-green-dim bg-green/5 px-3 py-2 text-green">The app exists. Install it on each account you want on the board.</p>
          )}
          {justCreated && provider.kind === 'gitlab' && (
            <p className="rounded-sm border border-green-dim bg-green/5 px-3 py-2 text-green">
              GitLab is authorized. Your groups are being found; switch the ones you want to Active below.
            </p>
          )}
          <ul className="flex flex-wrap gap-4">
            {provider.links.map((l) => (
              <li key={l.url}>
                <External href={l.url} className="underline underline-offset-4">
                  {l.label} ↗
                </External>
              </li>
            ))}
            {provider.kind === 'gitlab' && (
              <li>
                <a href="/setup/gitlab/start" className="underline underline-offset-4 hover:text-ink-100">
                  Authorize again
                </a>
              </li>
            )}
          </ul>
          {provider.kind === 'gitlab' && <GitlabWebhooks setup={settings.setup.gitlab} />}
          {provider.rateLimits.length > 0 && (
            <ul className="font-mono text-[11px] text-ink-400">
              {provider.rateLimits.map((r) => (
                <li key={r.scope}>
                  {r.scope}: {r.remaining}/{r.limit} calls left · resets in {untilMinutes(r.resetAt)}m
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : provider.kind === 'github' ? (
        <GithubSetup suggestedName={settings.setup.githubAppName} publicUrl={settings.publicUrl} />
      ) : (
        <GitlabSetupForm setup={settings.setup.gitlab} />
      )}
    </Panel>
  )
}

function GithubSetup({ suggestedName, publicUrl }: { suggestedName: string; publicUrl: string }) {
  const [name, setName] = useState(suggestedName)
  const [error, setError] = useState<string | null>(null)
  const create = async () => {
    try {
      const { action, manifest } = await fetchManifest(name)
      const form = document.createElement('form')
      form.method = 'post'
      form.action = action
      const field = document.createElement('input')
      field.type = 'hidden'
      field.name = 'manifest'
      field.value = JSON.stringify(manifest)
      form.append(field)
      document.body.append(form)
      form.submit()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <form
      className="mt-3 flex flex-col gap-3 text-sm"
      onSubmit={(e) => {
        e.preventDefault()
        void create()
      }}
    >
      <p className="text-ink-300">
        One click creates a read-only GitHub App owned by you, with webhooks pointed at <span className="font-mono text-xs">{publicUrl}</span>. Then you install it on each org.
      </p>
      <label className="flex flex-col gap-1">
        <span className={FIELD_LABEL}>App name (must be unique on GitHub)</span>
        <input value={name} onChange={(e) => setName(e.target.value)} className={FIELD} />
      </label>
      {error && <p className="font-mono text-xs text-red">{error}</p>}
      <div>
        <Button kind="solid" type="submit" disabled={name.trim().length < 3}>
          Create GitHub App
        </Button>
      </div>
      <p className="font-mono text-[10px] text-ink-400">Or set GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY and GITHUB_APP_WEBHOOK_SECRET in the environment.</p>
    </form>
  )
}

/**
 * GitLab has no one-click app manifest. The person creates an OAuth application
 * on GitLab, pastes its id and secret here, and authorizes it in the browser.
 */
function GitlabSetupForm({ setup }: { setup: GitlabSetup }) {
  const save = useSaveGitlabApp()
  const [clientId, setClientId] = useState(setup.app?.clientId ?? '')
  const [clientSecret, setClientSecret] = useState('')
  const [changing, setChanging] = useState(setup.app === null)
  const host = new URL(setup.url).host

  const connect = async () => {
    await save.mutateAsync({ clientId: clientId.trim(), clientSecret: clientSecret.trim() })
    location.assign('/setup/gitlab/start')
  }

  return (
    <div className="mt-3 flex flex-col gap-3 text-sm">
      {setup.app && !changing ? (
        <>
          <p className="text-ink-300">
            An OAuth application is on file{setup.app.source === 'environment' ? ' from the environment' : ''}. Authorize it on {host} to connect.
          </p>
          <div className="flex items-center gap-4">
            <a href="/setup/gitlab/start" className="rounded-sm bg-ink-100 px-3 py-1.5 font-mono text-[11px] uppercase tracking-wide text-ink-950 hover:bg-white">
              Authorize on {host}
            </a>
            {setup.app.source === 'setup' && (
              <button type="button" onClick={() => setChanging(true)} className="font-mono text-[10px] uppercase tracking-wide text-ink-400 hover:text-ink-100">
                Use a different application
              </button>
            )}
          </div>
        </>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            void connect()
          }}
        >
          <p className="text-ink-300">
            On {host}, add an application under your user settings or a group's settings. Give it the redirect URI below and the{' '}
            <span className="font-mono text-xs">read_api</span> scope, keep it confidential, and paste its id and secret here. Then you pick the groups to show.
          </p>
          <label className="flex flex-col gap-1">
            <span className={FIELD_LABEL}>Redirect URI</span>
            <input readOnly value={setup.redirectUri} onFocus={(e) => e.target.select()} className={FIELD} />
          </label>
          <label className="flex flex-col gap-1">
            <span className={FIELD_LABEL}>Application ID</span>
            <input value={clientId} onChange={(e) => setClientId(e.target.value)} className={FIELD} autoComplete="off" />
          </label>
          <label className="flex flex-col gap-1">
            <span className={FIELD_LABEL}>Secret</span>
            <input type="password" value={clientSecret} onChange={(e) => setClientSecret(e.target.value)} className={FIELD} autoComplete="off" />
          </label>
          {save.isError && <p className="font-mono text-xs text-red">{save.error.message}</p>}
          <div>
            <Button kind="solid" type="submit" disabled={save.isPending || clientId.trim() === '' || clientSecret.trim() === ''}>
              Connect GitLab
            </Button>
          </div>
        </form>
      )}
      <p className="font-mono text-[10px] text-ink-400">
        Or set GITLAB_URL, GITLAB_CLIENT_ID and GITLAB_CLIENT_SECRET in the environment, or GITLAB_TOKEN with GITLAB_TOKEN_ACCOUNTS for a quick look.
      </p>
    </div>
  )
}

/** GitLab has no app-level webhooks; a person adds one per group or project by hand, and this tells them what to paste. */
function GitlabWebhooks({ setup }: { setup: GitlabSetup }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="flex flex-col gap-2">
      <button type="button" onClick={() => setOpen(!open)} className="self-start font-mono text-[10px] uppercase tracking-wide text-ink-400 hover:text-ink-100">
        {open ? 'Hide' : 'Show'} webhook details
      </button>
      {open && (
        <div className="flex flex-col gap-2 rounded-sm border border-ink-700 p-3">
          <p className="text-xs text-ink-300">
            Optional. Polling catches everything within a few minutes; a webhook on a group or project refreshes a repo within seconds. Add one with these
            values and the pipeline, push, tag push, merge request, and release events.
          </p>
          <label className="flex flex-col gap-1">
            <span className={FIELD_LABEL}>URL</span>
            <input readOnly value={setup.webhookUrl} onFocus={(e) => e.target.select()} className={FIELD} />
          </label>
          <label className="flex flex-col gap-1">
            <span className={FIELD_LABEL}>Secret token</span>
            <input readOnly value={setup.webhookSecret ?? ''} onFocus={(e) => e.target.select()} className={FIELD} />
          </label>
        </div>
      )}
    </div>
  )
}

const STATUS_LABEL: Record<Account['status'], string> = { active: 'On the board', pending: 'Waiting for approval', ignored: 'Ignored' }

function AccountRow({ account }: { account: Account }) {
  const update = useUpdateAccount()
  return (
    <li className="grid items-center gap-x-4 gap-y-1 py-2.5 md:grid-cols-[24px_minmax(200px,1fr)_1fr_auto]">
      {account.avatarUrl ? <img src={account.avatarUrl} alt="" className="size-6 rounded-sm" /> : <span />}
      <span>
        {account.name} <span className="font-mono text-[11px] text-ink-400">{account.login}</span>
      </span>
      <span className={`font-mono text-[11px] ${account.status === 'pending' ? 'text-amber' : account.lastError ? 'text-red' : 'text-ink-400'}`}>
        {account.lastError ?? `${PROVIDER_LABELS[account.provider]} ${account.kind} · ${STATUS_LABEL[account.status]}`}
      </span>
      <select
        value={account.status}
        onChange={(e) => update.mutate({ id: account.id, status: e.target.value as Account['status'] })}
        className="rounded-sm border border-ink-600 bg-ink-850 px-2 py-1 font-mono text-[11px] uppercase tracking-wide text-ink-100 focus:border-ink-300 focus:outline-none"
      >
        <option value="active">Active</option>
        <option value="pending">Pending</option>
        <option value="ignored">Ignored</option>
      </select>
    </li>
  )
}

function Rules({ rules, defaults }: { rules: RuleSeverities; defaults: RuleSeverities }) {
  const update = useUpdateRules()
  const changed = RULE_KEYS.some((k) => rules[k] !== defaults[k])
  return (
    <section>
      <div className="flex items-baseline gap-4">
        <Kicker>Rules</Kicker>
        {changed && (
          <button type="button" onClick={() => update.mutate(defaults)} className="font-mono text-[10px] uppercase tracking-wide text-ink-400 hover:text-ink-100">
            Reset to defaults
          </button>
        )}
      </div>
      <p className="mt-2 max-w-2xl text-sm text-ink-300">
        Each rule is one reason a repo stops being green. Red means broken. Amber means look at it. Info shows without changing the color. Off hides it. Any repo can override these.
      </p>
      <ul className="mt-4 divide-y divide-ink-800 border-y border-ink-700">
        {RULE_KEYS.map((rule) => (
          <li key={rule} className="grid items-center gap-x-6 gap-y-1 py-3 md:grid-cols-[minmax(200px,1fr)_2fr_auto]">
            <span className="font-medium">{RULES[rule].label}</span>
            <span className="text-sm text-ink-300">{RULES[rule].description}</span>
            <SeveritySelect value={rules[rule]} onChange={(v) => v !== '' && update.mutate({ ...rules, [rule]: v })} />
          </li>
        ))}
      </ul>
    </section>
  )
}
