import type { ReactNode } from 'react'
import type { Level, Severity } from '../../shared/rules.ts'
import { href, onLinkClick, type Route } from '../router.ts'

export const LEVEL_LABEL: Record<Level, string> = { red: 'Red', amber: 'Amber', green: 'Green', quiet: 'Quiet', unknown: 'Unknown' }

export const LEVEL_TEXT: Record<Level, string> = {
  red: 'text-red',
  amber: 'text-amber',
  green: 'text-green',
  quiet: 'text-quiet',
  unknown: 'text-ink-400',
}

const LAMP: Record<Level | Severity, string> = {
  red: 'lamp-red',
  amber: 'lamp-amber',
  green: 'lamp-green',
  quiet: 'bg-quiet/60',
  unknown: 'bg-unknown',
  info: 'bg-ink-400',
  off: 'bg-ink-700',
}

export function Lamp({ level, size = 'md', className = '' }: { level: Level | Severity; size?: 'sm' | 'md' | 'lg'; className?: string }) {
  const dims = size === 'sm' ? 'size-1.5' : size === 'lg' ? 'size-3.5' : 'size-2.5'
  return <span aria-hidden className={`inline-block shrink-0 rounded-full ${dims} ${LAMP[level]} ${className}`} />
}

export function Link({ to, className = '', children, title }: { to: Route; className?: string; children: ReactNode; title?: string }) {
  return (
    <a href={href(to)} onClick={onLinkClick} className={className} title={title}>
      {children}
    </a>
  )
}

export function External({ href: url, className = '', children }: { href: string; className?: string; children: ReactNode }) {
  return (
    <a href={url} target="_blank" rel="noreferrer" className={`hover:text-ink-100 hover:underline underline-offset-4 ${className}`}>
      {children}
    </a>
  )
}

export function Button({
  children,
  onClick,
  kind = 'ghost',
  disabled,
  type = 'button',
}: {
  children: ReactNode
  onClick?: () => void
  kind?: 'ghost' | 'solid' | 'danger'
  disabled?: boolean
  type?: 'button' | 'submit'
}) {
  const styles = {
    ghost: 'border border-ink-600 text-ink-200 hover:border-ink-300 hover:text-ink-100 disabled:opacity-40',
    solid: 'bg-ink-100 text-ink-950 hover:bg-white disabled:opacity-40',
    danger: 'border border-red-dim text-red hover:border-red disabled:opacity-40',
  }[kind]
  return (
    <button type={type} onClick={onClick} disabled={disabled} className={`rounded-sm px-3 py-1.5 font-mono text-[11px] tracking-wide uppercase transition-colors ${styles}`}>
      {children}
    </button>
  )
}

export function Kicker({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`font-mono text-[10px] tracking-[0.18em] uppercase text-ink-400 ${className}`}>{children}</div>
}

export function Panel({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <section className={`rounded-md border border-ink-700 bg-ink-900/70 ${className}`}>{children}</section>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="rounded-md border border-dashed border-ink-700 px-6 py-12 text-center text-sm text-ink-400">{children}</div>
}

export function SeveritySelect({
  value,
  onChange,
  inherit,
}: {
  value: Severity | ''
  onChange: (value: Severity | '') => void
  /** Label for the empty option; omit to disallow it. */
  inherit?: string
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value as Severity | '')}
      className="rounded-sm border border-ink-600 bg-ink-850 px-2 py-1 font-mono text-[11px] uppercase tracking-wide text-ink-100 focus:border-ink-300 focus:outline-none"
    >
      {inherit !== undefined && <option value="">{inherit}</option>}
      <option value="red">Red</option>
      <option value="amber">Amber</option>
      <option value="info">Info</option>
      <option value="off">Off</option>
    </select>
  )
}
