import { useQueryClient } from '@tanstack/react-query'
import { useEstate, useLiveUpdates, useSweep } from './api.ts'
import { Board } from './components/Board.tsx'
import { Quiet } from './components/Quiet.tsx'
import { RepoDetail } from './components/RepoDetail.tsx'
import { Settings } from './components/Settings.tsx'
import { Lamp, Link } from './components/ui.tsx'
import { ago } from './format.ts'
import { useRoute, type Route } from './router.ts'

export function App() {
  const client = useQueryClient()
  useLiveUpdates(client)
  const route = useRoute()
  return (
    <div className="mx-auto flex min-h-screen max-w-[1500px] flex-col px-5 pb-16 md:px-8">
      <TopBar route={route} />
      <main className="flex-1">
        {route.page === 'board' && <Board />}
        {route.page === 'quiet' && <Quiet />}
        {route.page === 'settings' && <Settings />}
        {route.page === 'repo' && <RepoDetail id={route.id} />}
      </main>
    </div>
  )
}

function TopBar({ route }: { route: Route }) {
  const estate = useEstate()
  const sweep = useSweep()
  const sync = estate.data?.sync
  const totals = estate.data?.totals
  const nav = (page: Route['page'], label: string, to: Route) => (
    <Link
      to={to}
      className={`px-3 py-1.5 font-mono text-[11px] uppercase tracking-[0.14em] transition-colors ${
        route.page === page ? 'text-ink-100' : 'text-ink-400 hover:text-ink-200'
      }`}
    >
      {label}
    </Link>
  )
  return (
    <header className="flex flex-wrap items-center gap-x-6 gap-y-3 border-b border-ink-700 py-4">
      <Link to={{ page: 'board' }} className="flex items-center gap-2.5">
        <span className="flex items-center gap-1">
          <Lamp level="red" size="lg" />
          <Lamp level="green" size="lg" />
        </span>
        <span className="text-lg font-semibold tracking-tight">redgreen</span>
      </Link>
      <nav className="flex items-center">
        {nav('board', 'Board', { page: 'board' })}
        {nav('quiet', `Quiet${totals ? ` ${totals.quiet}` : ''}`, { page: 'quiet' })}
        {nav('settings', 'Settings', { page: 'settings' })}
      </nav>
      <div className="ml-auto flex items-center gap-4 font-mono text-[11px] text-ink-400">
        {sync && (
          <span className="flex items-center gap-2">
            <span className={`size-1.5 rounded-full ${sync.sweeping || sync.inFlight > 0 ? 'bg-amber' : 'bg-ink-600'}`} />
            {sync.sweeping || sync.inFlight > 0
              ? `syncing · ${sync.inFlight} live · ${sync.queued} queued`
              : `synced ${ago(sync.lastSweepFinishedAt)} ago`}
          </span>
        )}
        <button
          type="button"
          onClick={() => sweep.mutate()}
          disabled={sweep.isPending || sync?.sweeping}
          className="rounded-sm border border-ink-600 px-2.5 py-1 uppercase tracking-wide text-ink-200 transition-colors hover:border-ink-300 disabled:opacity-40"
        >
          Sync now
        </button>
      </div>
    </header>
  )
}
