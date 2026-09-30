import type { RepoDetailResponse } from '../../shared/api.ts'
import { PROVIDER_LABELS, type Pipeline, type ProviderKind, type PullRequest, type Run } from '../../shared/model.ts'
import { RULES, RULE_KEYS, type Severity } from '../../shared/rules.ts'
import { useRepo, useSettings, useSyncRepo, useUpdateRepoSettings } from '../api.ts'
import { ago, date, percent } from '../format.ts'
import { SignalLine } from './Board.tsx'
import { Button, Empty, External, Kicker, LEVEL_LABEL, LEVEL_TEXT, Lamp, Link, Panel, SeveritySelect } from './ui.tsx'

export function RepoDetail({ id }: { id: string }) {
  const repo = useRepo(id)
  if (repo.isPending) return <p className="py-16 text-center font-mono text-xs text-ink-400">Loading…</p>
  if (repo.isError) return <Empty>{repo.error.message}</Empty>
  return <Detail detail={repo.data} />
}

function Detail({ detail }: { detail: RepoDetailResponse }) {
  const { snapshot, health, settings } = detail
  const update = useUpdateRepoSettings(detail.id)
  const sync = useSyncRepo(detail.id)
  const level = health?.level ?? 'unknown'
  const segments = detail.fullName.split('/')
  const name = segments.at(-1)
  const owner = segments.slice(0, -1).join('/')

  return (
    <div className="flex flex-col gap-10 pt-8">
      <header className="flex flex-wrap items-start gap-x-8 gap-y-4">
        <div className="min-w-0 flex-1">
          <Kicker>
            <Link to={{ page: 'board' }} className="hover:text-ink-200">
              Board
            </Link>{' '}
            / {detail.account.login}
          </Kicker>
          <h1 className="mt-2 flex items-center gap-3 text-3xl font-semibold tracking-tight md:text-4xl">
            <Lamp level={level} size="lg" />
            <span className="truncate">
              <span className="text-ink-400">{owner}/</span>
              {name}
            </span>
          </h1>
          <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] text-ink-400">
            <span className={LEVEL_TEXT[level]}>{LEVEL_LABEL[level]}</span>
            {health?.quietReason && <span>{health.quietReason.replace('_', ' ')}</span>}
            {snapshot && (
              <>
                <span>{snapshot.visibility}</span>
                {snapshot.fork && <span>fork</span>}
                {snapshot.archived && <span>archived</span>}
                {snapshot.language && <span>{snapshot.language}</span>}
                <span>default {snapshot.defaultBranch}</span>
              </>
            )}
            {snapshot && <External href={snapshot.url}>open on {PROVIDER_LABELS[snapshot.provider]} ↗</External>}
          </p>
          {snapshot?.description && <p className="mt-3 max-w-2xl text-sm text-ink-300">{snapshot.description}</p>}
        </div>
        <div className="flex items-center gap-2">
          <Button onClick={() => sync.mutate()} disabled={sync.isPending}>
            {sync.isPending ? 'Syncing…' : 'Sync now'}
          </Button>
          <Button kind={settings.muted ? 'solid' : 'ghost'} onClick={() => update.mutate({ muted: !settings.muted })} disabled={update.isPending}>
            {settings.muted ? 'Unmute' : 'Mute repo'}
          </Button>
        </div>
      </header>

      {detail.syncError && (
        <p className="rounded-sm border border-red-dim bg-red/5 px-4 py-3 font-mono text-xs text-red">Last sync failed: {detail.syncError}</p>
      )}

      {health && health.signals.length > 0 && (
        <section>
          <Kicker>Signals</Kicker>
          <ul className="mt-3 flex flex-col gap-2">
            {health.signals.map((s) => (
              <li key={s.rule + s.pipelineId}>
                <SignalLine signal={s} />
              </li>
            ))}
          </ul>
        </section>
      )}

      {snapshot && health && (
        <dl className="grid grid-cols-2 gap-x-8 gap-y-4 border-y border-ink-700 py-4 font-mono text-[11px] text-ink-400 md:grid-cols-6">
          <Metric label="Success rate" value={percent(health.successRate) || '—'} />
          <Metric label="Last run" value={health.lastRunAt ? `${ago(health.lastRunAt)} ago` : '—'} />
          <Metric label="Pushed" value={snapshot.pushedAt ? `${ago(snapshot.pushedAt)} ago` : '—'} />
          <Metric label="Open PRs" value={String(snapshot.openPullRequests)} />
          <Metric label="Open issues" value={String(snapshot.openIssues)} />
          <Metric label="Stars" value={String(snapshot.stars)} />
        </dl>
      )}

      {snapshot ? (
        <div className="grid gap-10 lg:grid-cols-[2fr_1fr]">
          <div className="flex flex-col gap-10">
            <section>
              <Kicker>Pipelines · {snapshot.pipelines.length}</Kicker>
              {snapshot.pipelines.length === 0 ? (
                <p className="mt-3 text-sm text-ink-400">No pipelines. {snapshot.archived ? 'The repo is archived.' : 'Nothing runs here.'}</p>
              ) : (
                <ul className="mt-3 flex flex-col gap-2">
                  {snapshot.pipelines.map((p) => (
                    <PipelineRow
                      key={p.id}
                      pipeline={p}
                      defaultBranch={snapshot.defaultBranch}
                      muted={settings.mutedPipelines.includes(p.id)}
                      onToggleMute={() =>
                        update.mutate({
                          mutedPipelines: settings.mutedPipelines.includes(p.id)
                            ? settings.mutedPipelines.filter((m) => m !== p.id)
                            : [...settings.mutedPipelines, p.id],
                        })
                      }
                    />
                  ))}
                </ul>
              )}
            </section>

            <section>
              <Kicker>Open {snapshot.provider === 'gitlab' ? 'merge' : 'pull'} requests · {snapshot.openPullRequests}</Kicker>
              {snapshot.pullRequests.length === 0 ? (
                <p className="mt-3 text-sm text-ink-400">None.</p>
              ) : (
                <ul className="mt-3 divide-y divide-ink-800 border-y border-ink-700">
                  {snapshot.pullRequests.map((pr) => (
                    <PullRow key={pr.number} pr={pr} />
                  ))}
                </ul>
              )}
            </section>
          </div>

          <aside className="flex flex-col gap-8">
            <Panel className="p-4">
              <Kicker>Releases</Kicker>
              {snapshot.releases.length === 0 ? (
                <p className="mt-2 text-sm text-ink-400">None.</p>
              ) : (
                <ul className="mt-2 flex flex-col gap-1.5 text-sm">
                  {snapshot.releases.map((r) => (
                    <li key={r.tag} className="flex items-baseline justify-between gap-3">
                      <External href={r.url} className="truncate">
                        {r.name}
                      </External>
                      <span className="shrink-0 font-mono text-[11px] text-ink-400">
                        {r.prerelease ? 'pre · ' : ''}
                        {ago(r.publishedAt)} ago
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </Panel>

            <Panel className="p-4">
              <Kicker>Security</Kicker>
              <ul className="mt-2 flex flex-col gap-1.5 text-sm">
                <SecurityLine label={SECURITY_LABELS[snapshot.provider].dependabot} counts={snapshot.security.dependabot} />
                <SecurityLine label={SECURITY_LABELS[snapshot.provider].codeScanning} counts={snapshot.security.codeScanning} />
              </ul>
            </Panel>

            <Panel className="p-4">
              <Kicker>Rules for this repo</Kicker>
              <p className="mt-1 text-xs text-ink-400">Override the estate-wide severity for this repo only.</p>
              <RuleOverrides
                overrides={settings.ruleOverrides}
                onChange={(rule, value) => {
                  const next = { ...settings.ruleOverrides }
                  if (value === '') delete next[rule]
                  else next[rule] = value
                  update.mutate({ ruleOverrides: next })
                }}
              />
            </Panel>

            <Panel className="p-4">
              <Kicker>History</Kicker>
              <ul className="mt-2 flex flex-col gap-1 font-mono text-[11px]">
                {detail.history.map((h) => (
                  <li key={h.at} className="flex items-center gap-2 text-ink-300">
                    <Lamp level={h.level} size="sm" />
                    <span className={LEVEL_TEXT[h.level]}>{LEVEL_LABEL[h.level]}</span>
                    <span className="ml-auto text-ink-400">{date(h.at)}</span>
                  </li>
                ))}
                {detail.history.length === 0 && <li className="text-ink-400">No changes recorded yet.</li>}
              </ul>
              <p className="mt-3 font-mono text-[10px] text-ink-400">Synced {detail.syncedAt ? `${ago(detail.syncedAt)} ago` : 'never'}</p>
            </Panel>
          </aside>
        </div>
      ) : (
        <Empty>Not synced yet.</Empty>
      )}
    </div>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="uppercase tracking-[0.14em]">{label}</dt>
      <dd className="mt-0.5 text-base tabular-nums text-ink-100">{value}</dd>
    </div>
  )
}

const RUN_COLOR: Record<string, string> = {
  success: 'bg-green',
  failure: 'bg-red',
  timed_out: 'bg-red',
  startup_failure: 'bg-red',
  cancelled: 'bg-ink-600',
  skipped: 'bg-ink-700',
  neutral: 'bg-ink-600',
  stale: 'bg-ink-700',
  action_required: 'bg-amber',
}

function RunSquare({ run }: { run: Run }) {
  const color = run.status !== 'completed' ? 'bg-amber/60' : RUN_COLOR[run.conclusion ?? ''] ?? 'bg-ink-700'
  const label = `#${run.number} ${run.trigger} ${run.ref ?? ''} · ${run.status === 'completed' ? run.conclusion : run.status} · ${ago(run.createdAt)} ago`
  return (
    <a href={run.url} target="_blank" rel="noreferrer" title={label} className={`block h-4 w-2 rounded-[1px] ${color} hover:opacity-70`} />
  )
}

const STATE_LABEL: Record<Pipeline['state'], string> = { enabled: '', disabled: 'disabled', dormant: 'switched off by the forge' }

const SECURITY_LABELS: Record<ProviderKind, { dependabot: string; codeScanning: string }> = {
  github: { dependabot: 'Dependabot', codeScanning: 'Code scanning' },
  gitlab: { dependabot: 'Dependency scanning', codeScanning: 'SAST and secrets' },
}

function PipelineRow({
  pipeline,
  defaultBranch,
  muted,
  onToggleMute,
}: {
  pipeline: Pipeline
  defaultBranch: string
  muted: boolean
  onToggleMute: () => void
}) {
  const onDefault = pipeline.runs.filter((r) => r.ref === defaultBranch && r.trigger !== 'pull_request')
  const latest = onDefault[0]
  const dim = muted || pipeline.state === 'disabled'
  return (
    <li className={`rounded-sm border border-ink-700 bg-ink-900/60 px-4 py-3 ${dim ? 'opacity-50' : ''}`}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <External href={pipeline.url} className="font-medium">
          {pipeline.name}
        </External>
        <span className="font-mono text-[11px] text-ink-400">{pipeline.path.replace('.github/workflows/', '')}</span>
        {pipeline.schedules.map((s) => (
          <span key={`${s.cron} ${s.timezone}`} className="rounded-sm border border-ink-700 px-1.5 font-mono text-[10px] text-ink-300">
            ⏱ {s.cron}
            {s.timezone !== 'UTC' && ` ${s.timezone}`}
          </span>
        ))}
        {STATE_LABEL[pipeline.state] && <span className="font-mono text-[11px] text-amber">{STATE_LABEL[pipeline.state]}</span>}
        <button type="button" onClick={onToggleMute} className="ml-auto font-mono text-[10px] uppercase tracking-wide text-ink-400 hover:text-ink-100">
          {muted ? 'unmute' : 'mute'}
        </button>
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex gap-[3px]">
          {[...pipeline.runs].reverse().map((r) => (
            <RunSquare key={r.id} run={r} />
          ))}
          {pipeline.runs.length === 0 && <span className="font-mono text-[11px] text-ink-400">no runs</span>}
        </div>
        {latest && (
          <span className="font-mono text-[11px] text-ink-400">
            {defaultBranch}: <span className={latest.conclusion === 'success' ? 'text-green' : latest.conclusion === 'failure' ? 'text-red' : 'text-ink-200'}>{latest.status === 'completed' ? latest.conclusion : latest.status}</span>{' '}
            · {ago(latest.createdAt)} ago · {latest.trigger}
          </span>
        )}
      </div>
    </li>
  )
}

function PullRow({ pr }: { pr: PullRequest }) {
  const checks = { success: 'text-green', failure: 'text-red', pending: 'text-amber', none: 'text-ink-400' }[pr.checks]
  return (
    <li className="grid items-baseline gap-x-4 py-2 md:grid-cols-[auto_1fr_auto_auto]">
      <span className="font-mono text-[11px] text-ink-400">#{pr.number}</span>
      <External href={pr.url} className={`truncate text-sm ${pr.draft ? 'text-ink-400' : ''}`}>
        {pr.draft ? 'Draft: ' : ''}
        {pr.title}
      </External>
      <span className={`font-mono text-[11px] ${checks}`}>{pr.checks === 'none' ? 'no checks' : pr.checks}</span>
      <span className="font-mono text-[11px] text-ink-400">
        {pr.author ?? ''} · {ago(pr.updatedAt)} ago
      </span>
    </li>
  )
}

function SecurityLine({ label, counts }: { label: string; counts: { critical: number; high: number; url: string } | null }) {
  return (
    <li className="flex items-baseline justify-between gap-3">
      <span>{label}</span>
      {counts ? (
        <External href={counts.url} className="font-mono text-[11px]">
          <span className={counts.critical > 0 ? 'text-red' : 'text-ink-400'}>{counts.critical} critical</span>
          <span className="text-ink-400"> · </span>
          <span className={counts.high > 0 ? 'text-amber' : 'text-ink-400'}>{counts.high} high</span>
        </External>
      ) : (
        <span className="font-mono text-[11px] text-ink-400">not available</span>
      )}
    </li>
  )
}

function RuleOverrides({
  overrides,
  onChange,
}: {
  overrides: Partial<Record<keyof typeof RULES, Severity>>
  onChange: (rule: keyof typeof RULES, value: Severity | '') => void
}) {
  const settings = useSettings()
  return (
    <ul className="mt-3 flex flex-col gap-2">
      {RULE_KEYS.map((rule) => (
        <li key={rule} className="flex items-center justify-between gap-3 text-sm">
          <span>{RULES[rule].label}</span>
          <SeveritySelect
            value={overrides[rule] ?? ''}
            onChange={(v) => onChange(rule, v)}
            inherit={`Estate (${settings.data?.rules[rule] ?? RULES[rule].defaultSeverity})`}
          />
        </li>
      ))}
    </ul>
  )
}

