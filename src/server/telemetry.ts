// Traces and metrics over OTLP, on when OTEL_EXPORTER_OTLP_ENDPOINT is set
// and silent otherwise: the instruments below come from the OpenTelemetry
// API, which answers with no-ops until a provider is registered, so the
// code that measures never asks whether telemetry is on.

import { metrics, trace, SpanStatusCode, type Attributes, type Span } from '@opentelemetry/api'
import { OTLPMetricExporter } from '@opentelemetry/exporter-metrics-otlp-http'
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http'
import { resourceFromAttributes } from '@opentelemetry/resources'
import { MeterProvider, PeriodicExportingMetricReader } from '@opentelemetry/sdk-metrics'
import { BatchSpanProcessor, NodeTracerProvider } from '@opentelemetry/sdk-trace-node'
import type { Config } from './config.ts'
import type { Logger } from './log.ts'

const SERVICE = 'redgreen'

export interface Telemetry {
  /** Runs `fn` inside a span named `redgreen.outcomes.compute` and records how long it took, by window. */
  compute<T>(window: number, fn: () => T): T
  /** Counts a deployment kept for Outcomes, by where it came from and how it ended. */
  deploymentIngested(source: string, outcome: string): void
  /** Counts one read of an incident source, by source and whether it answered. */
  incidentsRead(source: string, outcome: 'ok' | 'failed'): void
  /** Counts a browser telemetry batch by what became of it. */
  browserBatch(outcome: 'relayed' | 'dropped' | 'refused' | 'failed'): void
  /** Records one served request by route pattern, method, and status. */
  request(route: string, method: string, status: number, seconds: number): void
  shutdown(): Promise<void>
}

/** Builds the instruments, and the exporters behind them when the endpoint is set. */
export function createTelemetry(config: Pick<Config, 'OTEL_EXPORTER_OTLP_ENDPOINT' | 'OTEL_SERVICE_NAME'>, version: string, log: Logger): Telemetry {
  const stop: (() => Promise<void>)[] = []
  if (config.OTEL_EXPORTER_OTLP_ENDPOINT) {
    const endpoint = config.OTEL_EXPORTER_OTLP_ENDPOINT.replace(/\/$/, '')
    const resource = resourceFromAttributes({ 'service.name': config.OTEL_SERVICE_NAME ?? SERVICE, 'service.version': version })
    const tracer = new NodeTracerProvider({ resource, spanProcessors: [new BatchSpanProcessor(new OTLPTraceExporter({ url: `${endpoint}/v1/traces` }))] })
    tracer.register()
    const meter = new MeterProvider({ resource, readers: [new PeriodicExportingMetricReader({ exporter: new OTLPMetricExporter({ url: `${endpoint}/v1/metrics` }), exportIntervalMillis: 30_000 })] })
    metrics.setGlobalMeterProvider(meter)
    stop.push(() => tracer.shutdown(), () => meter.shutdown())
    log.info('telemetry on', { endpoint, service: config.OTEL_SERVICE_NAME ?? SERVICE })
  } else {
    log.info('telemetry off: OTEL_EXPORTER_OTLP_ENDPOINT is not set')
  }

  const tracer = trace.getTracer(SERVICE, version)
  const meter = metrics.getMeter(SERVICE, version)
  meter.createObservableGauge(`${SERVICE}.build_info`, { description: 'Always 1, labelled with the running version.' }).addCallback((o) => o.observe(1, { version }))
  const computeDuration = meter.createHistogram(`${SERVICE}.outcomes.compute.duration`, { unit: 's', description: 'How long one Outcomes computation took.' })
  const ingested = meter.createCounter(`${SERVICE}.outcomes.deployments_ingested`, { description: 'Deployments kept for Outcomes, by source and outcome.' })
  const incidentsRead = meter.createCounter(`${SERVICE}.outcomes.incidents_read`, { description: 'Reads of an incident source, by source and outcome.' })
  const batches = meter.createCounter(`${SERVICE}.telemetry.batches`, { description: 'Browser telemetry batches, by what became of them.' })
  const requests = meter.createHistogram('http.server.request.duration', { unit: 's', description: 'Served requests by route, method, and status.' })

  return {
    compute(window, fn) {
      return tracer.startActiveSpan(`${SERVICE}.outcomes.compute`, { attributes: { window } }, (span: Span) => {
        const started = performance.now()
        try {
          return fn()
        } catch (error) {
          span.setStatus({ code: SpanStatusCode.ERROR, message: error instanceof Error ? error.message : String(error) })
          throw error
        } finally {
          const seconds = (performance.now() - started) / 1000
          span.setAttribute('duration_s', seconds)
          computeDuration.record(seconds, { window })
          span.end()
        }
      })
    },
    deploymentIngested(source, outcome) {
      ingested.add(1, { source, outcome } satisfies Attributes)
    },
    incidentsRead(source, outcome) {
      incidentsRead.add(1, { source, outcome })
    },
    browserBatch(outcome) {
      batches.add(1, { outcome })
    },
    request(route, method, status, seconds) {
      requests.record(seconds, { 'http.route': route, 'http.request.method': method, 'http.response.status_code': status })
    },
    async shutdown() {
      for (const s of stop) await s().catch((error: unknown) => log.warn('telemetry shutdown failed', { error }))
    },
  }
}
