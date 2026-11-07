// Package telemetry holds redgreen's logs, traces, and metrics. Logs are one
// JSON object per line on stdout. Traces and metrics go over OTLP/HTTP when
// OTEL_EXPORTER_OTLP_ENDPOINT is set; without it the instruments are no-ops,
// so the code that measures never asks whether telemetry is on.
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// NewLogger writes JSON lines to w: timestamp, level, msg, the trace id of
// the span in the context, and the fields given. Each part of the app adds
// its own component with With.
func NewLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) > 0 {
			return a
		}
		switch a.Key {
		case slog.TimeKey:
			a.Key = "timestamp"
		case slog.LevelKey:
			a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
		}
		return a
	}})
	return slog.New(traced{h})
}

// traced adds the trace id of the active span to every record.
type traced struct{ slog.Handler }

func (t traced) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return t.Handler.Handle(ctx, r)
}

func (t traced) WithAttrs(attrs []slog.Attr) slog.Handler { return traced{t.Handler.WithAttrs(attrs)} }
func (t traced) WithGroup(name string) slog.Handler       { return traced{t.Handler.WithGroup(name)} }
