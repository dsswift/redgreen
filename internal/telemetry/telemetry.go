package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Service is the name every span, metric, and log carries.
const Service = "redgreen"

// Quota is one rate limit a forge reported.
type Quota struct {
	Provider  string
	Remaining float64
	Limit     float64
}

// ActiveJobs is how many CI jobs share a repository, runner kind, OS, and
// status (queued or in_progress).
type ActiveJobs struct {
	Repo, Runner, OS, Status string
	N                        int
}

// Telemetry holds the instruments. The zero of every method is safe to call
// before Setup: the global providers answer with no-ops.
type Telemetry struct {
	tracer       trace.Tracer
	syncDuration metric.Float64Histogram
	webhookLag   metric.Float64Histogram
	webhooks     metric.Int64Counter
	pushes       metric.Int64Counter
	describes    metric.Int64Counter
	requests     metric.Float64Histogram
	jobs         metric.Int64Counter
	jobWait      metric.Float64Histogram
	jobRun       metric.Float64Histogram
	lastSweep    atomic.Int64
	activeJobs   atomic.Pointer[func() []ActiveJobs]
	quotas       atomic.Pointer[func() []Quota]
	shutdown     []func(context.Context) error
}

// Setup turns on OTLP export when endpoint is set and builds the
// instruments either way.
func Setup(ctx context.Context, endpoint, service, version string, log *slog.Logger) (*Telemetry, error) {
	t := &Telemetry{}
	if service == "" {
		service = Service
	}
	if endpoint != "" {
		res := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service), semconv.ServiceVersion(version))
		base := strings.TrimRight(endpoint, "/")
		traces, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(base+"/v1/traces"))
		if err != nil {
			return nil, err
		}
		metrics, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"))
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(traces))
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(30*time.Second))))
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		t.shutdown = append(t.shutdown, tp.Shutdown, mp.Shutdown)
		log.Info("telemetry on", "endpoint", base, "service", service)
	} else {
		log.Info("telemetry off: OTEL_EXPORTER_OTLP_ENDPOINT is not set")
	}
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.tracer = otel.Tracer(Service)
	m := otel.Meter(Service)
	var errs []error
	keep := func(err error) { errs = append(errs, err) }

	var err error
	t.syncDuration, err = m.Float64Histogram(Service+".sync.duration", metric.WithUnit("s"), metric.WithDescription("How long one repository sync took, by provider and outcome."))
	keep(err)
	t.webhookLag, err = m.Float64Histogram(Service+".webhook.lag", metric.WithUnit("s"), metric.WithDescription("From a webhook arriving to its repository pushed to Orrery, by provider."))
	keep(err)
	t.webhooks, err = m.Int64Counter(Service+".webhooks", metric.WithDescription("Webhook deliveries, by provider and outcome."))
	keep(err)
	t.pushes, err = m.Int64Counter(Service+".push.entities", metric.WithDescription("Entities written to Orrery, by blueprint and outcome."))
	keep(err)
	t.describes, err = m.Int64Counter(Service+".orrery.describe", metric.WithDescription("Writes of the app's own description to Orrery, by outcome."))
	keep(err)
	// A job waits seconds for a runner on a good day and an hour on a bad
	// one, and runs from seconds to hours.
	jobBuckets := metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200)
	t.jobs, err = m.Int64Counter(Service+".actions.jobs", metric.WithDescription("CI jobs finished, by repository, runner kind, OS, and conclusion."))
	keep(err)
	t.jobWait, err = m.Float64Histogram(Service+".actions.job.wait", metric.WithUnit("s"), jobBuckets, metric.WithDescription("From a CI job queued to a runner taking it, by repository, runner kind, and OS."))
	keep(err)
	t.jobRun, err = m.Float64Histogram(Service+".actions.job.duration", metric.WithUnit("s"), jobBuckets, metric.WithDescription("How long a CI job ran on its runner, by repository, runner kind, OS, and conclusion."))
	keep(err)
	t.requests, err = m.Float64Histogram("http.server.request.duration", metric.WithUnit("s"), metric.WithDescription("Served requests by route, method, and status."))
	keep(err)

	build, err := m.Int64ObservableGauge(Service+".build_info", metric.WithDescription("Always 1, labelled with the running version."))
	keep(err)
	quotaLeft, err := m.Float64ObservableGauge(Service+".quota.remaining", metric.WithDescription("Forge API calls left in the current rate-limit window, the lowest per provider."))
	keep(err)
	quotaLimit, err := m.Float64ObservableGauge(Service+".quota.limit", metric.WithDescription("The forge's rate limit for the window, per provider."))
	keep(err)
	active, err := m.Int64ObservableGauge(Service+".actions.jobs.active", metric.WithDescription("CI jobs waiting for a runner (queued) and running (in_progress) now, by repository, runner kind, and OS."))
	keep(err)
	sweep, err := m.Int64ObservableGauge(Service+".sweep.last_success", metric.WithUnit("s"), metric.WithDescription("Unix time the last sweep that reached every configured forge finished."))
	keep(err)
	cpu, err := m.Float64ObservableCounter("process.cpu.time", metric.WithUnit("s"), metric.WithDescription("CPU time the process used, by state."))
	keep(err)
	mem, err := m.Int64ObservableGauge("process.memory.usage", metric.WithUnit("By"), metric.WithDescription("Memory the process holds, by kind."))
	keep(err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(build, 1, metric.WithAttributes(attribute.String("version", version)))
		if at := t.lastSweep.Load(); at > 0 {
			o.ObserveInt64(sweep, at)
		}
		if f := t.activeJobs.Load(); f != nil {
			for _, a := range (*f)() {
				o.ObserveInt64(active, int64(a.N), metric.WithAttributes(attribute.String("repository", a.Repo), attribute.String("runner", a.Runner), attribute.String("os", a.OS), attribute.String("status", a.Status)))
			}
		}
		if f := t.quotas.Load(); f != nil {
			for _, q := range lowest((*f)()) {
				attrs := metric.WithAttributes(attribute.String("provider", q.Provider))
				o.ObserveFloat64(quotaLeft, q.Remaining, attrs)
				o.ObserveFloat64(quotaLimit, q.Limit, attrs)
			}
		}
		var ru syscall.Rusage
		if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
			o.ObserveFloat64(cpu, seconds(ru.Utime), metric.WithAttributes(attribute.String("process.cpu.state", "user")))
			o.ObserveFloat64(cpu, seconds(ru.Stime), metric.WithAttributes(attribute.String("process.cpu.state", "system")))
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		o.ObserveInt64(mem, int64(ms.HeapInuse), metric.WithAttributes(attribute.String("process.memory.kind", "heap")))
		o.ObserveInt64(mem, int64(ms.Sys), metric.WithAttributes(attribute.String("process.memory.kind", "total")))
		return nil
	}, build, quotaLeft, quotaLimit, active, sweep, cpu, mem)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func seconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1e6
}

// lowest keeps the scope with the least left for each provider.
func lowest(qs []Quota) []Quota {
	by := map[string]Quota{}
	for _, q := range qs {
		if have, ok := by[q.Provider]; !ok || q.Remaining < have.Remaining {
			by[q.Provider] = q
		}
	}
	out := make([]Quota, 0, len(by))
	for _, q := range by {
		out = append(out, q)
	}
	return out
}

// Shutdown flushes and stops the exporters.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, s := range t.shutdown {
		errs = append(errs, s(ctx))
	}
	return errors.Join(errs...)
}

// WatchQuotas reads the forges' rate limits on every export.
func (t *Telemetry) WatchQuotas(f func() []Quota) { t.quotas.Store(&f) }

// WatchJobs reads the CI jobs in flight on every export.
func (t *Telemetry) WatchJobs(f func() []ActiveJobs) { t.activeJobs.Store(&f) }

// JobStarted records how long a CI job waited for its runner.
func (t *Telemetry) JobStarted(ctx context.Context, repo, runner, os string, wait time.Duration) {
	t.jobWait.Record(ctx, wait.Seconds(), metric.WithAttributes(attribute.String("repository", repo), attribute.String("runner", runner), attribute.String("os", os)))
}

// JobFinished counts a finished CI job and, when it reached a runner, how
// long it ran.
func (t *Telemetry) JobFinished(ctx context.Context, repo, runner, os, conclusion string, ran time.Duration) {
	attrs := metric.WithAttributes(attribute.String("repository", repo), attribute.String("runner", runner), attribute.String("os", os), attribute.String("conclusion", conclusion))
	t.jobs.Add(ctx, 1, attrs)
	if ran > 0 {
		t.jobRun.Record(ctx, ran.Seconds(), attrs)
	}
}

// SweepSucceeded records the end of a sweep that reached every forge.
func (t *Telemetry) SweepSucceeded(at time.Time) { t.lastSweep.Store(at.Unix()) }

// Synced records one repository sync.
func (t *Telemetry) Synced(ctx context.Context, provider string, took time.Duration, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	t.syncDuration.Record(ctx, took.Seconds(), metric.WithAttributes(attribute.String("provider", provider), attribute.String("outcome", outcome)))
}

// Webhook counts one delivery by what became of it.
func (t *Telemetry) Webhook(ctx context.Context, provider, outcome string) {
	t.webhooks.Add(ctx, 1, metric.WithAttributes(attribute.String("provider", provider), attribute.String("outcome", outcome)))
}

// WebhookLag records the time from a delivery to its repository's push.
func (t *Telemetry) WebhookLag(ctx context.Context, provider string, lag time.Duration) {
	t.webhookLag.Record(ctx, lag.Seconds(), metric.WithAttributes(attribute.String("provider", provider)))
}

// Pushed counts entities written to Orrery: accepted, rejected, or failed.
func (t *Telemetry) Pushed(ctx context.Context, blueprint, outcome string, n int) {
	if n == 0 {
		return
	}
	t.pushes.Add(ctx, int64(n), metric.WithAttributes(attribute.String("blueprint", blueprint), attribute.String("outcome", outcome)))
}

// Described counts one write of redgreen's own description.
func (t *Telemetry) Described(ctx context.Context, outcome string) {
	t.describes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Start opens a span; a background task is a root span of its own.
func (t *Telemetry) Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
}

// Root opens a span with no parent, for a background task.
func (t *Telemetry) Root(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, trace.WithNewRoot(), trace.WithAttributes(attrs...))
}

// End closes a span, marking it failed when err is set.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Middleware makes every request a server span named by its route pattern,
// continues an incoming traceparent, and records its duration.
func (t *Telemetry) Middleware(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.tracer.Start(ctx, r.Method+" "+route, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.route", route), attribute.String("http.request.method", r.Method)))
		rec := &status{ResponseWriter: w, code: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(rec, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", rec.code))
		if rec.code >= 500 {
			span.SetStatus(codes.Error, http.StatusText(rec.code))
		}
		span.End()
		t.requests.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(
			attribute.String("http.route", route), attribute.String("http.request.method", r.Method), attribute.Int("http.response.status_code", rec.code)))
	})
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// Transport makes every outbound call a client span and sends traceparent.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base}
}

type roundTripper struct{ base http.RoundTripper }

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := otel.Tracer(Service).Start(r.Context(), r.Method+" "+r.URL.Host, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("server.address", r.URL.Host)))
	r = r.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))
	res, err := rt.base.RoundTrip(r)
	if err == nil {
		span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
		if res.StatusCode >= 500 {
			span.SetStatus(codes.Error, http.StatusText(res.StatusCode))
		}
	}
	End(span, err)
	return res, err
}
