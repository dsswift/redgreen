// A logo an app names in its own orrery.yaml (`icon: {name, file}`), checked
// the way the hub checks a custom icon before it is put there with
// `PUT /api/icons/{name}`. Both writers use it: the describer for redgreen's
// own logo, and the pusher for the logo of every repository it watches. Pure,
// so it is tested without a hub.

import { createHash } from 'node:crypto'
import { parse } from 'yaml'

/** A custom icon name the hub takes. */
export const ICON_NAME = /^[a-z0-9][a-z0-9_]{0,62}$/

/** The largest icon the hub takes. */
export const MAX_ICON_BYTES = 256 * 1024

const TYPES: Record<string, string> = { svg: 'image/svg+xml', png: 'image/png', webp: 'image/webp' }

/** What an orrery.yaml says about its logo. */
export interface IconRef {
  name: string
  /** The path from the repository root. */
  file: string
}

/** A logo ready to put: its name, its type from its extension, its bytes, and their hash. */
export interface IconFile {
  name: string
  type: string
  sha256: string
  data: Uint8Array
}

/** The logo a repository's orrery.yaml names for the service it describes. */
export interface RepoIcon extends IconFile {
  /** The `sw_service` key the descriptor names, lowercase. */
  service: string
}

/** The type a file is sent as, from its extension, or null for one the hub does not take. */
export function iconType(file: string): string | null {
  const ext = file.slice(file.lastIndexOf('.') + 1).toLowerCase()
  return file.includes('.') ? (TYPES[ext] ?? null) : null
}

/** A logo checked as the hub checks one. Throws with the reason a hub would refuse it. */
export function iconFile(ref: IconRef, data: Uint8Array): IconFile {
  if (!ICON_NAME.test(ref.name)) throw new Error(`icon name "${ref.name}" does not match ${ICON_NAME.source}`)
  const type = iconType(ref.file)
  if (!type) throw new Error(`icon file "${ref.file}" is not .svg, .png, or .webp`)
  if (data.byteLength === 0) throw new Error(`icon file "${ref.file}" is empty`)
  if (data.byteLength > MAX_ICON_BYTES) throw new Error(`icon file "${ref.file}" is ${data.byteLength} bytes, over ${MAX_ICON_BYTES}`)
  return { name: ref.name, type, sha256: createHash('sha256').update(data).digest('hex'), data }
}

/**
 * The service and logo another repository's orrery.yaml names. Only `service`
 * and `icon` are read; whatever else the file says is its app's business.
 * Null when it names no logo. Throws when it names one the wrong way.
 */
export function describedIcon(text: string): { service: string; icon: IconRef } | null {
  const doc: unknown = parse(text)
  if (!doc || typeof doc !== 'object' || !('icon' in doc) || doc.icon == null) return null
  const { service, icon } = doc as { service?: unknown; icon: unknown }
  if (typeof service !== 'string' || !service) throw new Error('orrery.yaml names an icon but no service')
  if (!icon || typeof icon !== 'object') throw new Error('orrery.yaml: icon needs a name and a file')
  const { name, file } = icon as { name?: unknown; file?: unknown }
  if (typeof name !== 'string' || typeof file !== 'string' || !name || !file) throw new Error('orrery.yaml: icon needs a name and a file')
  return { service, icon: { name, file } }
}
