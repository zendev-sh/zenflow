package main

// orchestrator_opts.go - turn parsed CLI flags into a slice of
// `zenflow.Option`s ready for `zenflow.New(...)`. Plus the coordinator
// wiring helpers that decide nil-coord / default coord / synthesise-
// only based on `--quiet` / `--summary-only`, the `installTracerFunc`
// + `withDefaultExporterFunc` test seams that the `-tags otel` build
// path overrides via init in trace_otel.go, and `startCoordRunner`
// (a thin CLI wrapper over zenflow.RunCoordinatorLoop, retained as a
// stable test seam for the cmdFlow / cmdGoal call sites and
// coordCleanupDelay override). cmdAgent intentionally never appends a
// WithCoordinator option (no coordinator semantics for single-agent
// runs).

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
	"github.com/zendev-sh/zenflow/cmd/zenflow/tool"
	"github.com/zendev-sh/zenflow/sink"
)

// buildOrchestratorOpts builds common orchestrator options from CLI flags.
// Handles LLM provider auto-detection, progress sink, model, concurrency,
// and tracing. Coordinator wiring is the caller's responsibility - see
// coordinatorOption which decides nil-coord vs default
// coord vs synthesize-only based on cmdFlags. cmdAgent has no
// coordinator semantics, so it intentionally never appends a
// WithCoordinator option.
func buildOrchestratorOpts(flags cmdFlags) []zenflow.Option {
	var progressSink zenflow.ProgressSink
	if flags.jsonOutput {
 // B7: JSONSink moved to public sink package (zenflow/sink/json.go)
 // so library consumers can construct it without importing cmd/zenflow.
		progressSink = sink.JSON(stdout)
	} else {
 // Bind sink to the package-level `stdout` writer so tests that
 // redirect stdout capture sink output too.
		progressSink = NewStdoutSinkTo(stdout, WithStdoutShowPlan(flags.showPlan), WithStdoutVerbose(flags.verbose))
	}
	opts := []zenflow.Option{zenflow.WithProgress(progressSink)}
	if flags.maxConcurrency > 0 {
		opts = append(opts, zenflow.WithMaxConcurrency(flags.maxConcurrency))
	}
	if flags.trace {
		opts = traceAppendOptionsFunc(opts)
	}

	// CLI consumers map ZENFLOW_AGENT_HANDLE_TTL onto the library option
	// (the library itself never reads env vars). Invalid / zero / negative
	// values fall back to DefaultAgentHandleTTL inside zenflow.
	if raw := os.Getenv("ZENFLOW_AGENT_HANDLE_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			opts = append(opts, zenflow.WithAgentHandleTTL(d))
		}
	}

	// Resolve LLM provider from --model flag (provider/model format) + env vars.
	// modelID is the bare model name (without provider prefix).
	if model, modelID := resolveProvider(flags.model); model != nil {
		opts = append(opts, zenflow.WithModel(model))
		if modelID != "" {
			opts = append(opts, zenflow.WithDefaultModel(modelID))
		}
	} else if flags.model != "" {
 // No provider resolved - pass raw model string as default.
		opts = append(opts, zenflow.WithDefaultModel(flags.model))
	}

	// Wire default tools for CLI execution with safe-by-default
	// containment. When --workdir is set, contain to that path. When
	// unset, contain to the current working directory at startup -
	// this prevents an LLM from writing to arbitrary absolute paths
	// outside where the user actually invoked the CLI. Library users
	// who need permissive tools should call tool.DefaultTools and
	// zenflow.WithTools themselves.
	var workdirAbs string
	if flags.workdir != "" {
		if abs, err := filepathAbs(flags.workdir); err == nil {
			workdirAbs = abs
		}
	} else if cwd, err := os.Getwd(); err == nil {
		workdirAbs = cwd
	}
	opts = append(opts, zenflow.WithTools(tool.DefaultToolsIn(workdirAbs)...))

	// Max retries: CLI flag overrides YAML options.maxRetries.
	if flags.maxRetries >= 0 {
		opts = append(opts, zenflow.WithGoAIOptions(goai.WithMaxRetries(flags.maxRetries)))
	}

	// --max-depth caps nested agent-tool spawning. cmdAgent installs
	// WithMaxDepth itself (its --max-depth predates parseFlags and is
	// stripped from common before parseFlags runs), so flags.maxDepth
	// is always 0 on the cmdAgent path here - the guard below is a
	// no-op for cmdAgent and only fires for cmdFlow / cmdGoal which
	// route the flag through parseFlags.
	if flags.maxDepth > 0 {
		opts = append(opts, zenflow.WithMaxDepth(flags.maxDepth))
	}

	// Thinking / extended reasoning. Uses per-request ProviderOptions so
	// the same flag works across all providers - each reads only the
	// keys it understands (bedrock: reasoningConfig; anthropic: thinking;
	// google: thinkingConfig; openai/azure: reasoning_effort).
	if po := thinkingProviderOptions(flags.thinking); po != nil {
		opts = append(opts, zenflow.WithGoAIOptions(goai.WithProviderOptions(po)))
	}

	// Output transform for context-aware truncation: rely on the library
	// default (TokenBudgetTransformer{MaxBytesPerDep: DefaultMaxBytesPerDep})
	// installed by zenflow.New when no WithOutputTransform option is set.
	// Tests / advanced consumers can still override with WithOutputTransform.

	if flags.stream {
		opts = append(opts, zenflow.WithStreaming())
	}
	if flags.verbose {
		opts = append(opts, zenflow.WithVerbose())
	}

	// Coordinator wiring. cmdAgent calls buildOrchestratorOpts but has
	// no coordinator semantics, so it must NOT receive a coord.
	// Coordinator installation is the flow/goal caller's responsibility
	// via coordinatorOption.
	return opts
}

// withDefaultExporterFunc is injectable for testing: it wraps the
// configured OTel exporter setup so unit tests can inject a failing
// or error-producing exporter without a real OTLP endpoint.
// Default implementation is a no-op: builds without `-tags otel` ship
// without the OpenTelemetry dependency closure (binaries from
// Homebrew / GoReleaser are built with `-tags otel`; bare `go install`
// drops the dep). The `-tags otel` build path overrides this var via
// init in trace_otel.go.
var withDefaultExporterFunc = func(_ context.Context) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}

// traceAppendOptionsFunc appends the OTel-related zenflow.Options when
// flags.trace is set. Default no-op (returns the slice unchanged) so
// builds without `-tags otel` skip the import. The `-tags otel` build
// overrides this var to append `zenotel.WithTracing` +
// `zenflow.WithGoAIOptions(zenotel.GoAIOption)`.
var traceAppendOptionsFunc = func(opts []zenflow.Option) []zenflow.Option {
	return opts
}

// installTracer installs a real OTel exporter as the global TracerProvider
// when flags.trace is set. Returns a shutdown func the caller must defer
// to flush buffered spans before process exit. If --trace is not set, or
// the binary was built without `-tags otel`, returns a no-op func.
// Must be called BEFORE buildOrchestratorOpts so the trace_otel build's
// real exporter wires into the global provider before the orchestrator
// reads it.
// installTracer is injectable for testing via the installTracerFunc var.
// The underlying exporter call is injectable via withDefaultExporterFunc.
var installTracerFunc = func(flags cmdFlags) func() {
	if !flags.trace {
		return func() {}
	}
	ctx := context.Background()
	shutdown, err := withDefaultExporterFunc(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "zenflow: --trace: failed to install exporter: %v\n", err)
		return func() {}
	}
	return func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutCtx); err != nil {
			fmt.Fprintf(stderr, "zenflow: --trace: exporter shutdown error: %v\n", err)
		}
	}
}

// coordinatorOption builds the coordinator wiring for cmdFlow / cmdGoal
// based on the parsed flags. Returns the option to install on the
// orchestrator and (when non-nil) the runner whose Run loop the caller
// is responsible for starting (per caller-owned lifecycle).
// Routing matrix:
// - --quiet → WithCoordinator(nil), nil runner
// - --summary-only → NewDefaultCoordRunner(llm, SynthesizeOnly)
// (coord drops narrate, keeps forward+finalize so it can
// emit one final synthesis via the finalize summary argument)
// - default → NewDefaultCoordRunner(llm)
// `--json` no longer disables coord. JSON consumers want the
// full event stream (including coord narration / forward / finalize)
// for programmatic processing - they consume each NDJSON line via
// shell pipes (`zenflow flow x.yaml --json | jq ...`). Hiding coord
// events from JSON consumers forced them to import zenflow as a
// library to get full observability. With `--json` keeping coord,
// CLI-based pipelines get the same event coverage as in-process use.
// Users who explicitly want JSON-without-coord-cost can combine
// `--quiet --json` (quiet still wins, coord disabled).
// When the LLM resolver returns nil (no provider configured) we also
// fall back to nil-coord - there is nothing for the coord to call.
func coordinatorOption(flags cmdFlags, llm provider.LanguageModel) (zenflow.Option, *zenflow.AgentRunner) {
	if llm == nil {
		return zenflow.WithCoordinator(nil), nil
	}
	if flags.quiet {
		return zenflow.WithCoordinator(nil), nil
	}
	if flags.summaryOnly {
		runner := zenflow.NewDefaultCoordRunner(llm, zenflow.SynthesizeOnly())
		return zenflow.WithCoordinator(runner), runner
	}
	runner := zenflow.NewDefaultCoordRunner(llm)
	return zenflow.WithCoordinator(runner), runner
}

// startCoordRunner is a thin CLI wrapper over zenflow.RunCoordinatorLoop
// kept around so existing callers (cmdFlow, cmdGoal) and unit tests can
// invoke a single CLI-package symbol while the heavy lifting (loop
// body, wake-blocking, cleanup timer) lives in the library. The
// coordCleanupDelay var is preserved as the test seam: tests that need
// a sub-second cleanup cap (e.g. exercising the timer-fired branch)
// continue to override it via t.Cleanup.
var startCoordRunner = func(ctx context.Context, runner *zenflow.AgentRunner, modelID string) func() {
	return zenflow.RunCoordinatorLoop(ctx, runner, modelID, zenflow.WithCleanupTimeout(coordCleanupDelay))
}

// coordCleanupDelay is injectable for testing so the cleanup-timeout branch
// can be exercised without waiting the full 2 s in slow CI environments.
var coordCleanupDelay = 2 * time.Second
