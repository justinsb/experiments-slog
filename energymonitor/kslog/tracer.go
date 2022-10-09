package kslog

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/exp/slog"
)

var alsoLogToStderr = slog.HandlerOptions{}.NewTextHandler(os.Stderr)

func Tracer(name string) *LogTracer {
	otelTracer := otel.Tracer(name)
	return &LogTracer{
		otel: otelTracer,
	}
}

type LogTracer struct {
	otel trace.Tracer
}

func (t *LogTracer) Start(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span, *slog.Logger) {
	ctx, span := t.otel.Start(ctx, spanName, opts...)
	// slogLogger := slog.FromContext(ctx)
	logHandler := &slogHandler{
		// inner: slogLogger,
		span: span,
	}
	slogLogger := slog.New(logHandler)

	ctx = slog.NewContext(ctx, slogLogger)
	return ctx, span, slogLogger
}

type slogHandler struct {
	opts slog.HandlerOptions
	span trace.Span
}

// Enabled reports whether the handler handles records at the given level.
// The handler ignores records whose level is lower.
func (h *slogHandler) Enabled(level slog.Level) bool {
	minLevel := slog.InfoLevel
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

// Handle handles the Record.
// Handle methods that produce output should observe the following rules:
//   - If r.Time() is the zero time, ignore the time.
//   - If an Attr's key is the empty string, ignore the Attr.
func (h *slogHandler) Handle(r slog.Record) error {
	if alsoLogToStderr.Enabled(r.Level()) {
		alsoLogToStderr.Handle(r)
	}

	var opts []trace.EventOption
	msg := r.Message()

	recordNumAttrs := r.NumAttrs()
	attrs := make([]attribute.KeyValue, 0, recordNumAttrs+1)

	{
		// level
		attrs = append(attrs, attribute.String("log.level", r.Level().String()))
	}

	// timestamp
	if t := r.Time(); !t.IsZero() {
		opts = append(opts, trace.WithTimestamp(t))
	}

	if recordNumAttrs != 0 {
		r.Attrs(func(attr slog.Attr) {
			valueKind := attr.Value.Kind()
			switch valueKind {
			case slog.StringKind:
				attrs = append(attrs, attribute.String(attr.Key, attr.Value.String()))
			case slog.Int64Kind:
				attrs = append(attrs, attribute.Int64(attr.Key, attr.Value.Int64()))
			case slog.Float64Kind:
				attrs = append(attrs, attribute.Float64(attr.Key, attr.Value.Float64()))
			// case slog.TimeKind:
			// 	attrs = append(attrs, attribute.Int64(attr.Key, attr.Value.Int64()))
			// case slog.AnyKind:
			// 	if tm, ok := v.any.(encoding.TextMarshaler); ok {
			// 		data, err := tm.MarshalText()
			// 		if err != nil {
			// 			return err
			// 		}
			// 		// TODO: avoid the conversion to string.
			// 		s.appendString(string(data))
			// 		return nil
			// 	}
			// 	s.appendString(fmt.Sprint(v.Any()))
			default:
				slog.Warn("unhandled value kind", "kind", valueKind.String())
				// *s.buf = v.append(*s.buf)
			}
		})
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	h.span.AddEvent(msg, opts...)

	return nil
}

// With returns a new Handler whose attributes consist of
// the receiver's attributes concatenated with the arguments.
// The Handler owns the slice: it may retain, modify or discard it.
func (h *slogHandler) With(attrs []slog.Attr) slog.Handler {
	return &slogHandler{
		opts: h.opts,
		span: h.span,
	}
}
