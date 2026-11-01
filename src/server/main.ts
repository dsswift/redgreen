import { serve } from '@hono/node-server'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { loadConfig } from './config.ts'
import { Describer, loadDescriptor } from './describe.ts'
import { Estate } from './estate.ts'
import { Events } from './events.ts'
import { createApp } from './http.ts'
import { createLogger } from './log.ts'
import { DispatchIncidents } from './outcomes/dispatch.ts'
import { OrreryReader } from './outcomes/orrery-reader.ts'
import { Outcomes } from './outcomes/outcomes.ts'
import { OrreryPusher } from './orrery.ts'
import { AzureDevopsProvider } from './providers/azuredevops/provider.ts'
import { GithubProvider } from './providers/github/provider.ts'
import { GitlabProvider } from './providers/gitlab/provider.ts'
import { SecretBox } from './secrets.ts'
import { Store } from './store.ts'
import { Scheduler } from './sync/scheduler.ts'
import { createTelemetry } from './telemetry.ts'
import pkg from '../../package.json' with { type: 'json' }

const log = createLogger('redgreen')
const config = loadConfig()
const dataDir = resolve(config.DATA_DIR)
const telemetry = createTelemetry(config, pkg.version, createLogger('telemetry'))
const describer = new Describer(config, loadDescriptor(), telemetry, createLogger('orrery-describe'))

const opening = Date.now()
const store = new Store(join(dataDir, 'redgreen.sqlite'))
log.info(store.cacheReset ? 'store opened; http cache reset' : 'store opened', { ms: Date.now() - opening })
const secrets = SecretBox.load(config.SECRET_KEY, dataDir, log)
const events = new Events()
const github = new GithubProvider(config, store, secrets, createLogger('github'))
const gitlab = new GitlabProvider(config, store, secrets, createLogger('gitlab'))
const azuredevops = new AzureDevopsProvider(config, store, secrets, createLogger('azuredevops'))
const providers = [github, gitlab, azuredevops]
const estate = new Estate(store, providers)
const outcomes = new Outcomes(store, providers, telemetry, createLogger('outcomes'))
const scheduler = new Scheduler(providers, store, estate, events, outcomes, config, createLogger('sync'))
const orrery = new OrreryPusher(config, store, estate, events, outcomes, createLogger('orrery'))
const orreryReader = new OrreryReader(config, store, outcomes, createLogger('orrery-reader'))
new DispatchIncidents(config, orreryReader, store, telemetry, createLogger('dispatch')).register()

const webDir = resolve(import.meta.dirname, '../../dist/web')
const app = createApp({
  config,
  store,
  estate,
  scheduler,
  events,
  providers,
  outcomes,
  telemetry,
  github,
  gitlab,
  azuredevops,
  log: createLogger('http'),
  webDir: existsSync(webDir) ? webDir : null,
})

const server = serve({ fetch: app.fetch, port: config.PORT, hostname: config.HOST }, (info) => {
  log.info('listening', { host: info.address, port: info.port, publicUrl: config.PUBLIC_URL, web: existsSync(webDir), dataDir })
  scheduler.start()
  orrery.start()
  orreryReader.start()
  describer.start()
})

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.on(signal, () => {
    log.info('shutting down', { signal })
    scheduler.stop()
    orrery.stop()
    orreryReader.stop()
    describer.stop()
    server.close(() => {
      store.close()
      void telemetry.shutdown().finally(() => process.exit(0))
    })
    // Open event streams would otherwise hold the server open indefinitely.
    if ('closeAllConnections' in server) server.closeAllConnections()
  })
}
