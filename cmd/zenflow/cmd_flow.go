package main

// cmd_flow.go - `zenflow flow <file> [<context>] ...` subcommand and
// the helper that prints the workflow's "final answer" (coord summary
// or the last terminal step's content) on interactive runs.

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/zendev-sh/zenflow"
)

func cmdFlow() {
	a := osArgs()
	if len(a) < 3 {
		fmt.Fprintln(stderr, "usage: zenflow flow <file> [<context>] [--model MODEL] [--timeout DURATION] [--max-concurrency N] [--max-depth N] [--resume RUN_ID] [--workdir DIR] [--json] [--quiet] [--summary-only] [--stream] [--plan] [--trace] [--thinking LEVEL] [--verbose] [--yolo] [--sandbox] [--allow LIST] [--deny LIST] [--strict]")
		exit(3)
		return
	}
	if argsContainHelp(a[2:]) {
		usage(stdout)
		return
	}
	// optional 2nd positional is the flow context distributed
	// to all steps via WithFlowContext. parseFlags consumes the
	// remaining args; positional[2:] is the flag region.
	flowContext, rawFlagArgs := splitPositionalContext(a[3:])
	// Strip permission flags before parseFlags so they don't trip the
	// "unknown flag" error. Permission flags are handled separately.
	flagArgs, perms, err := parsePermFlags(rawFlagArgs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	flags, err := parseFlags(flagArgs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	// Resolve workflow file BEFORE applyWorkdir so relative YAML paths work.
	wfPath := a[2]
	if !filepath.IsAbs(wfPath) {
		if abs, absErr := filepath.Abs(wfPath); absErr == nil {
			wfPath = abs
		}
	}
	if err := applyWorkdir(flags.workdir); err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	wf, err := zenflow.LoadWorkflow(wfPath)
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		exit(2)
		return
	}
	// Install OTel exporter BEFORE building orchestrator opts so that
	// zenotel.WithTracing inside buildOrchestratorOpts picks up the
	// registered global provider. Deferred shutdown flushes buffered spans.
	stopTracer := installTracerFunc(flags)
	defer stopTracer()
	// DAG rendering for --plan is handled by the sink via the
	// EventPlanReady event (StdoutSink.WithShowPlan). Don't print the
	// diagram here too - that double-renders. JSON sink users get the
	// workflow as a structured event instead.
	opts := buildOrchestratorOpts(flags)
	// When --model is set, force every per-agent/per-step model resolution
	// to flags.model. This enables cross-provider CLI testing:
	// `zenflow flow workflow.yaml --model gemini-2.5-flash` runs all
	// agents through Gemini regardless of what the YAML specifies.
	if flags.model != "" {
		opts = append(opts, zenflow.WithForceModel(flags.model))
	}
	// Wire FileStorage for persistence (enables --resume for future runs).
	storageDir := defaultStorageDir()
	opts = append(opts, zenflow.WithStorage(zenflow.NewFileStorage(storageDir)))
	// Wire permission handler when any permission flag is set (or always,
	// for TTY-based interactive prompts on unlisted tools).
	opts = append(opts, zenflow.WithPermissions(newCliPermissionHandler(perms, osStdin(), stderr, stdinIsTTY())))
	// coordinator wiring based on flags.
	llm, modelID := resolveProvider(flags.model)
	coordOpt, coordRunner := coordinatorOption(flags, llm)
	opts = append(opts, coordOpt)
	orch := newOrch(opts...)
	if !orch.HasLLM() {
		fmt.Fprintln(stderr, "no LLM model configured: pass --model MODEL (e.g. --model google/gemini-2.0-flash) or set ZENFLOW_MODEL=PROVIDER/MODEL in the environment")
		exit(3)
		return
	}
	ctx := context.Background()
	// install platform-aware signal handler so Ctrl+C cleanly
	// cancels the orchestrator on every supported OS (POSIX gets the
	// full SIGINT/SIGTERM/SIGHUP set; Windows gets os.Interrupt only).
	ctx, stopSignals := installSignalHandler(ctx)
	defer stopSignals()
	if flags.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, flags.timeout)
		defer cancel()
	}
	// start the coord Run loop on its own goroutine. cmdFlow
	// owns the lifecycle; the deferred cleanup signals the coord to
	// wind down when RunFlow returns.
	stopCoord := startCoordRunner(ctx, coordRunner, modelID)
	defer stopCoord()
	// propagate the optional 2nd positional as the flow context.
	var runOpts []zenflow.RunFlowOption
	if flowContext != "" {
		runOpts = append(runOpts, zenflow.WithFlowContext(flowContext))
	}
	var result *zenflow.WorkflowResult
	runWithWatchdog(flags.timeout, func() {
		if flags.resume != "" {
			result, err = runResumeFlow(orch, ctx, flags.resume, wf)
		} else {
			result, err = runFlow(orch, ctx, wf, runOpts...)
		}
	})
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		exit(1)
		return
	}
	// Print run ID for future --resume reference.
	if result.RunID != "" && !flags.jsonOutput {
		fmt.Fprintf(stdout, "Run ID: %s\n", result.RunID)
	}
	// display the workflow's final answer for cmdFlow when
	// stdout is in interactive mode (not --json / --quiet /
	// --summary-only). Pick (in order): coord-finalized Summary, or
	// the LAST topological step's Content. Without this, users have
	// no way to see the verdict / summarizer output without piping
	// to --json or rerunning with --resume - the live progress
	// stream shows step lifecycle events but never the actual text.
	if !flags.jsonOutput && !flags.quiet && !flags.summaryOnly {
		finalText := result.FinalAnswer(wf)
		if finalText != "" {
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "── Final answer ─────────────────────")
			fmt.Fprintln(stdout, finalText)
			fmt.Fprintln(stdout, "─────────────────────────────────────")
		}
	}
	if result.Status == zenflow.StatusFailed || result.Status == zenflow.StatusPartial {
		exit(1)
	}
}
