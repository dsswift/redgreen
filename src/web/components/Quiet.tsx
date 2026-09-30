import type { RepoCard } from '../../shared/api.ts'
import type { QuietReason } from '../../shared/rules.ts'
import { useEstate, useUpdateRepoSettings } from '../api.ts'
import { ago } from '../format.ts'
import { Button, Empty, Kicker, Link } from './ui.tsx'

const REASON: Record<QuietReason, string> = { archived: 'Archived', muted: 'Muted', no_pipelines: 'No pipelines' }
const ORDER: QuietReason[] = ['muted', 'no_pipelines', 'archived']

export function Quiet() {
  const estate = useEstate()
  if (estate.isPending) return <p className="py-16 text-center font-mono text-xs text-ink-400">Loading…</p>
  if (estate.isError) return <Empty>Could not load: {estate.error.message}</Empty>
  const quiet = estate.data.repos.filter((r) => r.level === 'quiet')
  const accounts = new Map(estate.data.accounts.map((a) => [a.id, a]))

  return (
    <div className="flex flex-col gap-8 pt-8">
      <div>
        <Kicker>Quiet</Kicker>
        <h1 className="mt-2 text-3xl font-semibold tracking-tight">
          {quiet.length} {quiet.length === 1 ? 'repo' : 'repos'} that never turn the board red
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-ink-300">
          Archived repos, repos with no pipelines, and repos you muted. A repo with no pipelines still shows up on the board if a critical security alert opens against it.
        </p>
      </div>
      {ORDER.map((reason) => {
        const rows = quiet.filter((r) => r.quietReason === reason)
        if (rows.length === 0) return null
        return (
          <section key={reason}>
            <Kicker className="mb-2">
              {REASON[reason]} · {rows.length}
            </Kicker>
            <ul className="divide-y divide-ink-800 border-y border-ink-700">
              {rows.map((repo) => (
                <Row key={repo.id} repo={repo} account={accounts.get(repo.accountId)?.login ?? ''} />
              ))}
            </ul>
          </section>
        )
      })}
      {quiet.length === 0 && <Empty>Nothing is quiet.</Empty>}
    </div>
  )
}

function Row({ repo, account }: { repo: RepoCard; account: string }) {
  const update = useUpdateRepoSettings(repo.id)
  return (
    <li className="grid items-baseline gap-x-6 gap-y-1 py-2.5 md:grid-cols-[minmax(240px,1fr)_1fr_auto_auto]">
      <Link to={{ page: 'repo', id: repo.id }} className="truncate hover:underline underline-offset-4">
        <span className="text-ink-400">{account}/</span>
        {repo.name}
      </Link>
      <span className="truncate font-mono text-[11px] text-ink-400">
        {[repo.archived && 'archived', repo.fork && 'fork', repo.visibility, repo.language].filter(Boolean).join(' · ')}
      </span>
      <span className="font-mono text-[11px] text-ink-400">pushed {ago(repo.pushedAt)} ago</span>
      <span className="justify-self-end">
        {repo.muted && (
          <Button onClick={() => update.mutate({ muted: false })} disabled={update.isPending}>
            Unmute
          </Button>
        )}
      </span>
    </li>
  )
}
