import { useState } from 'react'
import type { OutcomesResponse } from '../../shared/api.ts'
import { WINDOWS, failureRateBand, frequencyBand, leadTimeBand, restoreBand, type Band, type Measures, type OutcomeRow, type WindowDays } from '../../shared/outcomes.ts'
import { useOutcomes } from '../api.ts'
import { ago } from '../format.ts'
import { trackWindowPick } from '../telemetry.ts'
import { Empty, Kicker, Link } from './ui.tsx'

type MeasureKey = 'frequency' | 'leadTime' | 'changeFailureRate' | 'timeToRestore'

interface MeasureSpec {
  key: MeasureKey
  label: string
  short: string
  value: (m: Measures) => number | null
  format: (v: number | null) => string
  band: (v: number) => Band
  /** True when a larger number is better. */
  upIsGood: boolean
}

const MEASURES: MeasureSpec[] = [
  { key: 'frequency', label: 'Deployment frequency', short: 'Frequency', value: (m) => (m.deployments > 0 ? m.frequencyPerDay : null), format: frequency, band: frequencyBand, upIsGood: true },
  { key: 'leadTime', label: 'Lead time for changes', short: 'Lead time', value: (m) => m.leadTimeP50Hours, format: hours, band: leadTimeBand, upIsGood: false },
  { key: 'changeFailureRate', label: 'Change failure rate', short: 'Failure rate', value: (m) => m.changeFailureRate, format: (v) => (v === null ? '–' : `${Math.round(v * 100)}%`), band: failureRateBand, upIsGood: false },
  { key: 'timeToRestore', label: 'Time to restore', short: 'Restore', value: (m) => m.timeToRestoreP50Hours, format: hours, band: restoreBand, upIsGood: false },
]

const BAND_TEXT: Record<Band, string> = { elite: 'text-green', high: 'text-green', medium: 'text-amber', low: 'text-red' }
const BAND_LABEL: Record<Band, string> = { elite: 'Elite', high: 'High', medium: 'Medium', low: 'Low' }

/** "2.1/day", "3/wk", "1/mo". */
function frequency(perDay: number | null): string {
  if (perDay === null) return '–'
  if (perDay >= 1) return `${round(perDay)}/day`
  if (perDay * 7 >= 1) return `${round(perDay * 7)}/wk`
  return `${round(perDay * 30)}/mo`
}

/** "42m", "3.5h", "2.1d". */
function hours(h: number | null): string {
  if (h === null) return '–'
  if (h < 1) return `${Math.round(h * 60)}m`
  if (h < 48) return `${round(h)}h`
  return `${round(h / 24)}d`
}

const round = (n: number) => (n >= 10 ? Math.round(n) : Math.round(n * 10) / 10).toString()

export function Outcomes() {
  const [window, setWindow] = useState<WindowDays>(30)
  const [sort, setSort] = useState<{ key: MeasureKey | 'name'; desc: boolean }>({ key: 'name', desc: false })
  const query = useOutcomes(window)

  const pick = (w: WindowDays) => {
    setWindow(w)
    trackWindowPick(w)
  }

  if (query.isPending) return <p className="py-16 text-center font-mono text-xs text-ink-400">Counting deployments…</p>
  if (query.isError) return <Empty>Could not load outcomes: {query.error.message}</Empty>
  const data = query.data
  const rows = sortRows(data.rows, sort)

  return (
    <div className="flex flex-col gap-10 pt-8">
      <section className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <Kicker>Outcomes · last {window} days</Kicker>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight md:text-4xl">How changes reach production</h1>
          <p className="mt-2 max-w-2xl text-sm text-ink-400">
            The four DORA measures from {data.estate.current.deployments} deployments{data.sources.orrery ? ', read from the forges and from Orrery' : ', read from the forges'}
            {data.sources.incidents ? ' and incidents' : ''}. Each number is set against the {window} days before.
          </p>
        </div>
        <WindowPicker value={window} onChange={pick} />
      </section>

      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {MEASURES.map((spec) => (
          <StatTile key={spec.key} spec={spec} current={data.estate.current} previous={data.estate.previous} window={window} />
        ))}
      </section>

      <section>
        <Kicker className="mb-3">Per service</Kicker>
        {rows.length === 0 ? (
          <Empty>No deployments in the last {window * 2} days. Deployments come from GitHub and GitLab environments, and from Orrery when a read token is set.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[820px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-ink-700 font-mono text-[10px] uppercase tracking-[0.14em] text-ink-400">
                  <Header label="Service" active={sort.key === 'name'} desc={sort.desc} onClick={() => toggle(setSort, 'name', false)} align="left" />
                  {MEASURES.map((spec) => (
                    <Header key={spec.key} label={spec.short} active={sort.key === spec.key} desc={sort.desc} onClick={() => toggle(setSort, spec.key, spec.upIsGood)} align="right" />
                  ))}
                  <th className="py-2 pl-4 text-right font-normal">Grade</th>
                  <th className="py-2 pl-4 text-right font-normal">Last</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-ink-800">
                {rows.map((row) => (
                  <Row key={`${row.repoId}\n${row.service}`} row={row} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}

function toggle(set: (f: (s: { key: MeasureKey | 'name'; desc: boolean }) => { key: MeasureKey | 'name'; desc: boolean }) => void, key: MeasureKey | 'name', descFirst: boolean) {
  set((s) => ({ key, desc: s.key === key ? !s.desc : descFirst }))
}

function sortRows(rows: OutcomeRow[], sort: { key: MeasureKey | 'name'; desc: boolean }): OutcomeRow[] {
  const spec = MEASURES.find((m) => m.key === sort.key)
  const sorted = [...rows].sort((a, b) => {
    if (!spec) return `${a.fullName}/${a.service}`.localeCompare(`${b.fullName}/${b.service}`)
    const av = spec.value(a.current)
    const bv = spec.value(b.current)
    // Rows without a number sit at the end either way.
    if (av === null && bv === null) return 0
    if (av === null) return 1
    if (bv === null) return -1
    return av - bv
  })
  if (!sort.desc) return sorted
  const numbered = sorted.filter((r) => !spec || spec.value(r.current) !== null).reverse()
  return [...numbered, ...sorted.filter((r) => spec && spec.value(r.current) === null)]
}

function WindowPicker({ value, onChange }: { value: WindowDays; onChange: (w: WindowDays) => void }) {
  return (
    <div className="flex gap-1" role="radiogroup" aria-label="Window">
      {WINDOWS.map((w) => (
        <button
          key={w}
          type="button"
          role="radio"
          aria-checked={value === w}
          onClick={() => onChange(w)}
          className={`rounded-sm border px-2.5 py-1 font-mono text-[11px] uppercase tracking-wide transition-colors ${value === w ? 'border-ink-300 text-ink-100' : 'border-ink-700 text-ink-400 hover:text-ink-200'}`}
        >
          {w}d
        </button>
      ))}
    </div>
  )
}

function Header({ label, active, desc, onClick, align }: { label: string; active: boolean; desc: boolean; onClick: () => void; align: 'left' | 'right' }) {
  return (
    <th className={`py-2 font-normal ${align === 'right' ? 'pl-4 text-right' : 'pr-4 text-left'}`} aria-sort={active ? (desc ? 'descending' : 'ascending') : 'none'}>
      <button type="button" onClick={onClick} className={`uppercase tracking-[0.14em] transition-colors hover:text-ink-200 ${active ? 'text-ink-100' : ''}`}>
        {label}
        {active ? (desc ? ' ↓' : ' ↑') : ''}
      </button>
    </th>
  )
}

function StatTile({ spec, current, previous, window }: { spec: MeasureSpec; current: Measures; previous: Measures; window: WindowDays }) {
  const now = spec.value(current)
  const before = spec.value(previous)
  const band = now === null ? null : spec.band(now)
  return (
    <div className="rounded-md border border-ink-700 bg-ink-900/70 px-4 py-3">
      <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-ink-400">{spec.label}</div>
      <div className="mt-2 flex items-baseline gap-3">
        <span className="font-mono text-3xl tabular-nums text-ink-100">{spec.format(now)}</span>
        {band && <span className={`font-mono text-[11px] uppercase tracking-wide ${BAND_TEXT[band]}`}>{BAND_LABEL[band]}</span>}
      </div>
      <Delta now={now} before={before} spec={spec} window={window} />
    </div>
  )
}

/** The change against the previous window, coloured by whether it is good. */
function Delta({ now, before, spec, window }: { now: number | null; before: number | null; spec: MeasureSpec; window: WindowDays }) {
  if (now === null || before === null) return <div className="mt-1 font-mono text-[11px] text-ink-400">{before === null ? `nothing in the ${window} days before` : 'nothing this window'}</div>
  const diff = now - before
  const same = Math.abs(diff) < 1e-9
  const good = same ? null : diff > 0 === spec.upIsGood
  const arrow = same ? '→' : diff > 0 ? '↑' : '↓'
  const tone = good === null ? 'text-ink-400' : good ? 'text-green' : 'text-red'
  return (
    <div className={`mt-1 font-mono text-[11px] ${tone}`}>
      <span aria-hidden>{arrow}</span> {same ? 'same as' : `${spec.format(before)} in`} the {window} days before
    </div>
  )
}

function Row({ row }: { row: OutcomeRow }) {
  const owner = row.fullName.split('/').slice(0, -1).join('/')
  const grade = row.current.grade
  return (
    <tr>
      <td className="py-2 pr-4">
        <Link to={{ page: 'repo', id: row.repoId }} className="font-medium hover:underline underline-offset-4">
          <span className="text-ink-400">{owner}/</span>
          {row.fullName.split('/').at(-1)}
        </Link>
        {row.service && <span className="ml-2 font-mono text-[11px] text-ink-400">{row.service}</span>}
        <span className="ml-2 font-mono text-[10px] uppercase tracking-wide text-ink-600">{row.sources.join(' + ')}</span>
      </td>
      {MEASURES.map((spec) => (
        <Cell key={spec.key} spec={spec} row={row} />
      ))}
      <td className={`py-2 pl-4 text-right font-mono text-[11px] uppercase tracking-wide ${grade ? BAND_TEXT[grade] : 'text-ink-600'}`}>{grade ? BAND_LABEL[grade] : '–'}</td>
      <td className="py-2 pl-4 text-right font-mono text-[11px] text-ink-400">{row.lastDeployedAt ? `${ago(row.lastDeployedAt)} ago` : ''}</td>
    </tr>
  )
}

function Cell({ spec, row }: { spec: MeasureSpec; row: OutcomeRow }) {
  const value = spec.value(row.current)
  const series = row.series[spec.key]
  return (
    <td className="py-2 pl-4 text-right">
      <div className="flex items-center justify-end gap-3">
        <Sparkline values={series} title={`${spec.label}: ${series.map((v) => (v === null ? '–' : spec.format(v))).join(', ')}`} />
        <span className="w-14 font-mono tabular-nums text-ink-100">{spec.format(value)}</span>
      </div>
    </td>
  )
}

const W = 64
const H = 18

/** One thin line across the window, no axis, the latest point marked. A bucket with nothing leaves a gap. */
export function Sparkline({ values, title }: { values: (number | null)[]; title: string }) {
  const known = values.filter((v): v is number => v !== null)
  if (known.length === 0) return <span className="inline-block" style={{ width: W, height: H }} aria-hidden />
  const max = Math.max(...known)
  const min = Math.min(...known, 0)
  const span = max - min || 1
  const x = (i: number) => (values.length === 1 ? W / 2 : (i / (values.length - 1)) * (W - 2) + 1)
  const y = (v: number) => H - 1 - ((v - min) / span) * (H - 2)
  const segments: string[] = []
  let current: string[] = []
  values.forEach((v, i) => {
    if (v === null) {
      if (current.length) segments.push(current.join(' '))
      current = []
    } else current.push(`${current.length ? 'L' : 'M'}${x(i).toFixed(1)} ${y(v).toFixed(1)}`)
  })
  if (current.length) segments.push(current.join(' '))
  const lastIndex = values.length - 1 - [...values].reverse().findIndex((v) => v !== null)
  const last = values[lastIndex]
  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={title} className="shrink-0 overflow-visible">
      <title>{title}</title>
      {segments.map((d, i) => (
        <path key={i} d={d} fill="none" stroke="var(--color-ink-400)" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
      ))}
      {last !== null && last !== undefined && <circle cx={x(lastIndex)} cy={y(last)} r="2" fill="var(--color-ink-100)" />}
    </svg>
  )
}
