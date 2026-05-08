package main

// cmd_goal.go - `zenflow goal <goal> [<extra-context>] ...`
// subcommand and the `isCoordinatorError` helper used to choose the
// exit code (validation error vs step error) when goal decomposition
// fails.

import (
	"context"
	"errors"
	"fmt"

	"github.com/zendev-sh/zenflow"
)

func cmdGoal() {
	a := osArgs()
	if len(a) < 3 {
		fmt.Fprintln(stderr, "usage: zenflow goal <goal> [<extra-context>] [--model MODEL] [--max-concurrency N] [--max-depth N] [--timeout DURATION] [--workdir DIR] [--json] [--quiet] [--summary-only] [--stream] [--trace] [--thinking LEVEL] [--verbose] [--yolo] [--sandbox] [--allow LIST] [--deny LIST] [--strict]")
		exit(3)
		return
	}
	if argsContainHelp(a[2:]) {
		usage(stdout)
		return
	}
	goal := a[2]
	// optional 2nd positional is extra context appended to the
	// coordinator-decomposition prompt via WithGoalContext.
	goalContext, rawFlagArgs := splitPositionalContext(a[3:])
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
	if flags.resume != "" {
		fmt.Fprintln(stderr, "--resume is not supported for goal command (use with flow)")
		exit(3)
		return
	}
	if flags.showPlan {
		fmt.Fprintln(stderr, "--plan is not supported for goal command (use with flow)")
		exit(3)
		return
	}
	if extra, err := readPipedStdin(); err != nil {
		fmt.Fprintf(stderr, "reading stdin: %v\n", err)
		exit(3)
		return
	} else if extra != "" {
		goal += "\n\n" + extra
	}
	if err := applyWorkdir(flags.workdir); err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	// Install OTel exporter BEFORE building orchestrator opts so that
	// zenotel.WithTracing inside buildOrchestratorOpts picks up the
	// registered global provider. Deferred shutdown flushes buffered spans.
	stopTracer := installTracerFunc(flags)
	defer stopTracer()
	opts := buildOrchestratorOpts(flags)
	// Wire permission handler (interactive TTY prompts or flag-based pre-approval).
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
	stopCoord := startCoordRunner(ctx, coordRunner, modelID)
	defer stopCoord()
	// propagate the optional 2nd positional as the goal extra context.
	var runOpts []zenflow.RunGoalOption
	if goalContext != "" {
		runOpts = append(runOpts, zenflow.WithGoalContext(goalContext))
	}
	var result *zenflow.WorkflowResult
	runWithWatchdog(flags.timeout, func() {
		result, err = runGoal(orch, ctx, goal, runOpts...)
	})
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		if isCoordinatorError(err) {
			exit(2)
			return
		}
		exit(1)
		return
	}
	if result.Status == zenflow.StatusFailed || result.Status == zenflow.StatusPartial {
		exit(1)
	}
}

// isCoordinatorError returns true if the error is from coordinator parsing,
// validation, or tool validation - not from step execution.
func isCoordinatorError(err error) bool {
	var jsonErr *zenflow.JSONParseError
	var valErr *zenflow.CoordinatorValidationError
	var toolErr *zenflow.ToolNotFoundError
	return errors.As(err, &jsonErr) || errors.As(err, &valErr) || errors.As(err, &toolErr)
}
