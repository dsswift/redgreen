import { serve } from '@hono/node-server'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { loadConfig } from './config.ts'
import { Estate } from './estate.ts'
import { Events } from './events.ts'
import { createApp } from './http.ts'
import { createLogger } from './log.ts'
import { GithubProvider } from './providers/github/provider.ts'
import { GitlabProvider } from './providers/gitlab/provider.ts'
import { SecretBox } from './secrets.ts'
import { Store } from './store.ts'
import { Scheduler } from './sync/scheduler.ts'

const log = createLogger('redgreen')
const config = loadConfig()
const dataDir = resolve(config.DATA_DIR)

const store = new Store(join(dataDir, 'redgreen.sqlite'))
const secrets = SecretBox.load(config.SECRET_KEY, dataDir, log)
const events = new Events()
const github = new GithubProvider(config, store, secrets, createLogger('github'))
const gitlab = new GitlabProvider(config, store, secrets, createLogger('gitlab'))
const providers = [github, gitlab]
const estate = new Estate(store, providers)
const scheduler = new Scheduler(providers, store, estate, events, config, createLogger('sync'))

const webDir = resolve(import.meta.dirname, '../../dist/web')
const app = createApp({
  config,
  store,
  estate,
  scheduler,
  events,
  providers,
  github,
  gitlab,
  log: createLogger('http'),
  webDir: existsSync(webDir) ? webDir : null,
})

const server = serve({ fetch: app.fetch, port: config.PORT, hostname: config.HOST }, (info) => {
  log.info('listening', { host: info.address, port: info.port, publicUrl: config.PUBLIC_URL, web: existsSync(webDir), dataDir })
  scheduler.start()
})

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.on(signal, () => {
    log.info('shutting down', { signal })
    scheduler.stop()
    server.close(() => {
      store.close()
      process.exit(0)
    })
    // Open event streams would otherwise hold the server open indefinitely.
    if ('closeAllConnections' in server) server.closeAllConnections()
  })
}
