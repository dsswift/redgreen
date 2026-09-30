// One JSON object per line on stdout. Every decision that changes an outcome
// logs both branches, so a missing line is evidence that the path did not run.

type Level = 'debug' | 'info' | 'warn' | 'error'
type Fields = Record<string, unknown>

const LEVELS: Record<Level, number> = { debug: 10, info: 20, warn: 30, error: 40 }
const threshold = LEVELS[(process.env.LOG_LEVEL as Level | undefined) ?? 'info'] ?? LEVELS.info

export interface Logger {
  debug(msg: string, fields?: Fields): void
  info(msg: string, fields?: Fields): void
  warn(msg: string, fields?: Fields): void
  error(msg: string, fields?: Fields): void
  child(fields: Fields): Logger
}

export function createLogger(component: string, bound: Fields = {}): Logger {
  const write = (level: Level, msg: string, fields?: Fields) => {
    if (LEVELS[level] < threshold) return
    const line = { ts: new Date().toISOString(), level, component, msg, ...bound, ...fields }
    process.stdout.write(JSON.stringify(line, errorReplacer) + '\n')
  }
  return {
    debug: (msg, fields) => write('debug', msg, fields),
    info: (msg, fields) => write('info', msg, fields),
    warn: (msg, fields) => write('warn', msg, fields),
    error: (msg, fields) => write('error', msg, fields),
    child: (fields) => createLogger(component, { ...bound, ...fields }),
  }
}

function errorReplacer(_key: string, value: unknown): unknown {
  if (value instanceof Error) {
    const status = 'status' in value ? { status: value.status } : {}
    return { name: value.name, message: value.message, ...status }
  }
  return value
}
