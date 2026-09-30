import { useState } from 'react'
import type { AccountSummary, EstateResponse, RepoCard } from '../../shared/api.ts'
import type { Level, Signal } from '../../shared/rules.ts'
import { useEstate } from '../api.ts'
import { ago, compact, duration } from '../format.ts'
import { Empty, External, Kicker, LEVEL_LABEL, LEVEL_TEXT, Lamp, Link } from './ui.tsx'

const BOARD_LEVELS: Level[] = ['red', 'amber', 'green', 'unknown']

export function Board() {
  const estate = useEstate()
  const [query, setQuery] = useState('')
  const [only, setOnly] = useState<Level | null>(null)

  if (estate.isPending) return <p className="py-16 text-center font-mono text-xs text-ink-400">Loading the estate…</p>
  if (estate.isError) return <Empty>Could not load the estate: {estate.error.message}</Empty>
  const data = estate.data

  return (
    <div className="flex flex-col gap-10 pt-8">
      <Headline data={data} />
      <SetupNotices data={data} />
      <Attention repos={data.repos} />
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center gap-3">
          <Kicker className="mr-2">Estate</Kicker>
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Filter repos"
            className="w-56 rounded-sm border border-ink-700 bg-ink-900 px-3 py-1.5 font-mono text-xs text-ink-100 placeholder:text-ink-400 focus:border-ink-400 focus:outline-none"
          />
          <div className="flex gap-1">
            {BOARD_LEVELS.map((level) => (
              <button
                key={level}
                type="button"
                onClick={() => setOnly(only === level ? null : level)}
                className={`flex items-center gap-1.5 rounded-sm border px-2 py-1 font-mono text-[10px] uppercase tracking-wide transition-colors ${
                  only === level ? 'border-ink-300 text-ink-100' : 'border-ink-700 text-ink-400 hover:text-ink-200'
                }`}
              >
                <Lamp level={level} size="sm" /> {data.totals[level]}
              </button>
            ))}
          </div>
        </div>
        {data.accounts
          .filter((a) => a.status === 'active')
          .map((account) => (
            <AccountSection
              key={account.id}
              account={account}
              repos={data.repos.filter(
                (r) =>
                  r.accountId === account.id &&
                  r.level !== 'quiet' &&
                  (only === null || r.level === only) &&
                  (query === '' || r.fullName.toLowerCase().includes(query.toLowerCase())),
              )}
            />
          ))}
        {data.accounts.length === 0 && <Empty>No accounts yet. Connect GitHub in Settings.</Empty>}
      </div>
    </div>
  )
}

function Headline({ data }: { data: EstateResponse }) {
  const t = data.totals
  const mood = t.red > 0 ? 'red' : t.amber > 0 ? 'amber' : t.green > 0 ? 'green' : 'unknown'
  const line =
    t.red > 0
      ? `${t.red} ${t.red === 1 ? 'repo needs' : 'repos need'} you`
      : t.amber > 0
        ? `Nothing on fire. ${t.amber} to look at.`
        : t.green > 0
          ? 'All clear.'
          : 'Nothing synced yet.'
  return (
    <section className="grid gap-8 md:grid-cols-[1fr_auto] md:items-end">
      <div>
        <Kicker>Right now</Kicker>
        <h1 className={`mt-2 text-4xl font-semibold tracking-tight md:text-6xl ${LEVEL_TEXT[mood]}`}>{line}</h1>
        <div className="mt-6 flex flex-wrap gap-x-10 gap-y-4">
          {(['red', 'amber', 'green', 'quiet'] as Level[]).map((level) => (
            <div key={level} className="flex items-baseline gap-3">
              <Lamp level={level} size="lg" className="translate-y-[-2px]" />
              <span className={`font-mono text-3xl tabular-nums ${t[level] > 0 ? LEVEL_TEXT[level] : 'text-ink-600'}`}>{t[level]}</span>
              <span className="font-mono text-[11px] uppercase tracking-[0.14em] text-ink-400">{LEVEL_LABEL[level]}</span>
            </div>
          ))}
        </div>
      </div>
      <dl className="grid grid-cols-3 gap-x-8 gap-y-3 border-l border-ink-700 pl-6 font-mono text-[11px] text-ink-400 md:grid-cols-3">
        <Stat label="Repos" value={t.repos} />
        <Stat label="Pipelines" value={t.pipelines} />
        <Stat label="Runs · 24h" value={t.runsLast24h} />
        <Stat label="Releases · 7d" value={t.releasesLast7d} />
        <Stat label="Open PRs" value={t.openPullRequests} />
        <Stat label="Open issues" value={t.openIssues} />
      </dl>
    </section>
  )
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <dt className="uppercase tracking-[0.14em]">{label}</dt>
      <dd className="mt-0.5 text-lg tabular-nums text-ink-100">{compact(value)}</dd>
    </div>
  )
}

function SetupNotices({ data }: { data: EstateResponse }) {
  const provider = data.providers.find((p) => !p.configured)
  const pending = data.accounts.filter((a) => a.status === 'pending')
  if (!provider && pending.length === 0) return null
  return (
    <div className="flex flex-col gap-2">
      {provider && (
        <Notice>
          GitHub is not connected yet.{' '}
          <Link to={{ page: 'settings' }} className="underline underline-offset-4">
            Set it up in Settings.
          </Link>
        </Notice>
      )}
      {pending.length > 0 && (
        <Notice>
          {pending.length === 1 ? 'An account installed the app and is' : `${pending.length} accounts installed the app and are`} waiting for approval:{' '}
          {pending.map((a) => a.login).join(', ')}.{' '}
          <Link to={{ page: 'settings' }} className="underline underline-offset-4">
            Review in Settings.
          </Link>
        </Notice>
      )}
    </div>
  )
}

function Notice({ children }: { children: React.ReactNode }) {
  return <p className="rounded-sm border border-amber-dim bg-amber/5 px-4 py-3 text-sm text-amber">{children}</p>
}

function Attention({ repos }: { repos: RepoCard[] }) {
  const hot = repos.filter((r) => r.level === 'red' || r.level === 'amber')
  if (hot.length === 0) return null
  return (
    <section>
      <Kicker>Needs attention</Kicker>
      <ol className="mt-3 divide-y divide-ink-800 border-y border-ink-700">
        {hot.map((repo) => (
          <li key={repo.id} className="grid gap-x-6 gap-y-1 py-3 md:grid-cols-[14px_minmax(200px,1fr)_2fr_auto] md:items-baseline">
            <Lamp level={repo.level} className="translate-y-[-1px]" />
            <Link to={{ page: 'repo', id: repo.id }} className="truncate font-medium hover:underline underline-offset-4">
              <span className="text-ink-400">{repo.fullName.split('/')[0]}/</span>
              {repo.name}
            </Link>
            <div className="flex flex-col gap-0.5">
              {repo.signals
                .filter((s) => s.severity !== 'info')
                .map((s) => (
                  <SignalLine key={s.rule + s.pipelineId} signal={s} />
                ))}
            </div>
            <span className="font-mono text-[11px] text-ink-400">{repo.levelSince ? `${LEVEL_LABEL[repo.level].toLowerCase()} for ${duration(repo.levelSince)}` : ''}</span>
          </li>
        ))}
      </ol>
    </section>
  )
}

export function SignalLine({ signal }: { signal: Signal }) {
  const since = signal.since ? ` · ${signal.sinceIsLowerBound ? 'over ' : ''}${duration(signal.since)}` : ''
  return (
    <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 text-sm">
      <Lamp level={signal.severity} size="sm" className="translate-y-[-2px]" />
      {signal.url ? <External href={signal.url}>{signal.title}</External> : <span>{signal.title}</span>}
      <span className="line-clamp-1 font-mono text-[11px] text-ink-400" title={signal.detail}>
        {signal.detail}
        {since}
      </span>
    </span>
  )
}

function AccountSection({ account, repos }: { account: AccountSummary; repos: RepoCard[] }) {
  return (
    <section>
      <header className="flex items-center gap-3 py-2">
        {account.avatarUrl && <img src={account.avatarUrl} alt="" className="size-6 rounded-sm" />}
        <h2 className="font-medium">{account.name}</h2>
        <span className="font-mono text-[11px] text-ink-400">{account.login}</span>
        <span className="ml-auto flex items-center gap-3 font-mono text-[11px] text-ink-400">
          {(['red', 'amber', 'green'] as Level[]).map((level) => (
            <span key={level} className="flex items-center gap-1.5">
              <Lamp level={level} size="sm" /> {account.counts[level]}
            </span>
          ))}
          <span>{account.counts.quiet} quiet</span>
        </span>
        {account.lastError && <span className="font-mono text-[11px] text-red">{account.lastError}</span>}
      </header>
      {repos.length === 0 ? (
        <p className="py-3 font-mono text-[11px] text-ink-400">Nothing here.</p>
      ) : (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(170px,1fr))] gap-1.5">
          {repos.map((repo) => (
            <Tile key={repo.id} repo={repo} />
          ))}
        </ul>
      )}
    </section>
  )
}

const TILE_BORDER: Record<Level, string> = {
  red: 'border-red/50 hover:border-red',
  amber: 'border-amber/50 hover:border-amber',
  green: 'border-ink-700 hover:border-green/60',
  quiet: 'border-ink-800',
  unknown: 'border-ink-800 hover:border-ink-600',
}

function Tile({ repo }: { repo: RepoCard }) {
  const top = repo.signals[0]
  const meta = repo.syncError
    ? 'sync failed'
    : top
      ? top.title
      : repo.level === 'unknown'
        ? 'not synced yet'
        : repo.lastRunAt
          ? `last run ${ago(repo.lastRunAt)} ago`
          : 'no runs'
  return (
    <li>
      <Link
        to={{ page: 'repo', id: repo.id }}
        title={`${repo.fullName}\n${meta}`}
        className={`flex h-full flex-col gap-1.5 rounded-sm border bg-ink-900/60 px-3 py-2.5 transition-colors ${TILE_BORDER[repo.level]}`}
      >
        <span className="flex items-center gap-2">
          <Lamp level={repo.level} size="sm" />
          <span className="truncate text-sm font-medium">{repo.name}</span>
          {repo.running && <span className="ml-auto font-mono text-[9px] uppercase tracking-wide text-amber">run</span>}
        </span>
        <span className="truncate font-mono text-[10px] text-ink-400">{meta}</span>
      </Link>
    </li>
  )
}
