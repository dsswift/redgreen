// Browser telemetry through the Grafana Faro SDK. The page posts to its own
// server, which relays with the key or drops the batch; telemetry never
// fails the page. Events name what a person did on purpose, `<thing>.<verb>`,
// with values from the app's fixed lists.

import { faro, getWebInstrumentations, initializeFaro, type Faro } from '@grafana/faro-web-sdk'
import { TracingInstrumentation } from '@grafana/faro-web-tracing'

declare const __APP_VERSION__: string

let started: Faro | null = null

export function startTelemetry(): void {
  if (started) return
  try {
    started = initializeFaro({
      url: '/api/telemetry',
      app: { name: 'redgreen-web', version: __APP_VERSION__ },
      instrumentations: [...getWebInstrumentations({ captureConsole: false }), new TracingInstrumentation()],
      // Spans cover the app's own API; the telemetry endpoint is ignored by Faro itself, the event stream here.
      ignoreUrls: [/\/api\/events/],
      trackResources: false,
    })
  } catch (error) {
    console.warn('telemetry did not start', error)
  }
}

/** A person picked a window on the Outcomes page. */
export function trackWindowPick(window: number): void {
  if (!started) return
  faro.api.pushEvent('outcomes.window.pick', { window: String(window) })
}
