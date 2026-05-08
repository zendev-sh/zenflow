// Package otel provides OpenTelemetry tracing for zenflow workflows.
// It bridges goai's per-LLM-call tracing with zenflow's Orchestrator-level lifecycle,
// creating a hierarchical span structure:
//	RunFlow:
// zenflow.flow
// ├── zenflow.step (step A)
// │ └── chat ... ← goai otel
// ├── zenflow.coordinator {phase=on_step_event} ← after step A
// ├── zenflow.step (step B)
// │ └── chat ...
// ├── zenflow.coordinator {phase=on_step_event} ← after step B
// └── zenflow.coordinator {phase=synthesize} ← after all steps
//	RunGoal:
// zenflow.goal {zenflow.run_id, zenflow.goal.text} ← Orchestrator
// └── zenflow.flow {zenflow.run_id, zenflow.workflow.name} ← Orchestrator (via RunFlow)
// ├── zenflow.step ...
// ├── zenflow.coordinator {phase=on_step_event} ...
// └── zenflow.coordinator {phase=synthesize}
//	RunAgent:
// zenflow.agent {zenflow.run_id, zenflow.agent.prompt} ← Orchestrator
// └── chat ... ← goai otel
// Usage:
//	import (// zenotel "github.com/zendev-sh/zenflow/observability/otel"
//)
//	orch := zenflow.New(// zenflow.WithModel(model),
// zenflow.WithGoAIOptions(zenotel.GoAIOption),
// zenotel.WithTracing,
//)
package otel

import (
	"context"
	"os"

	"github.com/zendev-sh/goai"
	goaiotel "github.com/zendev-sh/goai/observability/otel"
	"github.com/zendev-sh/zenflow"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/zendev-sh/zenflow/observability/otel"

// TracingOption configures WithTracing and GoAIOption.
type TracingOption func(*config)

type config struct {
	tracerProvider trace.TracerProvider
}

// WithTracerProvider sets a custom TracerProvider (default: global).
func WithTracerProvider(tp trace.TracerProvider) TracingOption {
	return func(c *config) { c.tracerProvider = tp }
}

// WithDefaultExporter registers a global TracerProvider backed by a real
// exporter so spans are visible when --trace is set. The caller must invoke
// the returned shutdown func (typically deferred in main) to flush buffered
// spans before process exit.
// Exporter selection (in order of precedence):
// 1. OTEL_EXPORTER_OTLP_ENDPOINT is set: use OTLP/HTTP exporter directed
// at that endpoint. Useful for Jaeger, Grafana Tempo, OTEL Collector, etc.
// 2. Otherwise: write spans as JSON to w (typically os.Stderr) via
// stdouttrace so --trace produces visible output with zero config.
// The returned TracerProvider is also registered as the global OTel provider,
// so any call to otel.GetTracerProvider (e.g. inside WithTracing) picks it
// up automatically.
func WithDefaultExporter(ctx context.Context, w *os.File) (tp *sdktrace.TracerProvider, shutdown func(context.Context) error, err error) {
	var exp sdktrace.SpanExporter
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		exp, err = otlptracehttp.New(ctx)
		if err != nil {
			return nil, nil, err
		}
	} else {
		exp, err = stdouttrace.New(
			stdouttrace.WithWriter(w),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, err
		}
	}
	tp = sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	otel.SetTracerProvider(tp)
	return tp, tp.Shutdown, nil
}

// WithTracing returns a zenflow.Option that enables OTel tracing for
// all execution modes. Creates spans for zenflow.flow, zenflow.goal,
// zenflow.agent, zenflow.step, and zenflow.coordinator (when a
// coordinator runner is configured). GoAI LLM/tool spans are created
// separately via GoAIOption passed to the adapter.
func WithTracing(opts ...TracingOption) zenflow.Option {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.tracerProvider == nil {
		cfg.tracerProvider = otel.GetTracerProvider()
	}

	return zenflow.WithTracer(&otelTracer{
		tracer: cfg.tracerProvider.Tracer(tracerName),
	})
}

// GoAIOption returns a goai.Option that enables OTel tracing for
// individual LLM calls and tool executions. Pass this to WithGoAIOptions:
//	zenflow.WithGoAIOptions(zenotel.GoAIOption)
func GoAIOption(opts ...TracingOption) goai.Option {
	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}
	var goaiOpts []goaiotel.TracingOption
	if cfg.tracerProvider != nil {
		goaiOpts = append(goaiOpts, goaiotel.WithTracerProvider(cfg.tracerProvider))
	}
	return goaiotel.WithTracing(goaiOpts...)
}

// otelTracer implements zenflow.Tracer using OTel spans.
// Stateless - span lifecycle is managed via context propagation.
type otelTracer struct {
	tracer trace.Tracer
}

// - compile-time assertion that *otelTracer satisfies the
// zenflow.Tracer contract. Catches signature drift on either side
// at the type definition rather than at the (*otelTracer)(nil)
// assignment in NewTracer.
var _ zenflow.Tracer = (*otelTracer)(nil)

func (t *otelTracer) StartSpan(ctx context.Context, name string, attrs map[string]string) context.Context {
	otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		otelAttrs = append(otelAttrs, attribute.String(k, v))
	}
	ctx, _ = t.tracer.Start(ctx, name, trace.WithAttributes(otelAttrs...))
	return ctx
}

func (t *otelTracer) EndSpan(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}
