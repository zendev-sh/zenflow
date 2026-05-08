package main

// cmd_agent.go - `zenflow agent <prompt> ...` subcommand. Single-
// agent run; intentionally never installs a coordinator (no DAG, no
// step events). Has its own --max-turns / --max-depth parser inline
// because those flags predate parseFlags and are stripped out of the
// common arg slice before the shared parser runs.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/zendev-sh/zenflow"
)

func cmdAgent() {
	a := osArgs()
	if len(a) < 3 {
		fmt.Fprintln(stderr, "usage: zenflow agent <prompt> [--model MODEL] [--max-turns N] [--max-depth N] [--workdir DIR] [--json] [--stream] [--thinking LEVEL] [--verbose] [--yolo] [--sandbox] [--allow LIST] [--deny LIST] [--strict]")
		exit(3)
		return
	}
	if argsContainHelp(a[2:]) {
		usage(stdout)
		return
	}
	prompt := a[2]
	var (
		maxTurns int
		maxDepth int
		common   []string
	)
	for i := 3; i < len(a); i++ {
		switch a[i] {
		case "--max-turns":
			i++
			if i >= len(a) {
				fmt.Fprintln(stderr, "--max-turns requires a value")
				exit(3)
				return
			}
			n, err := strconv.Atoi(a[i])
			if err != nil {
				fmt.Fprintf(stderr, "invalid max-turns: %v\n", err)
				exit(3)
				return
			}
			if n < 0 {
				fmt.Fprintf(stderr, "--max-turns must not be negative: %d\n", n)
				exit(3)
				return
			}
			maxTurns = n
		case "--max-depth":
			i++
			if i >= len(a) {
				fmt.Fprintln(stderr, "--max-depth requires a value")
				exit(3)
				return
			}
			n, err := strconv.Atoi(a[i])
			if err != nil {
				fmt.Fprintf(stderr, "invalid max-depth: %v\n", err)
				exit(3)
				return
			}
			if n < 0 {
				fmt.Fprintf(stderr, "--max-depth must not be negative: %d\n", n)
				exit(3)
				return
			}
			maxDepth = n
		default:
			common = append(common, a[i])
		}
	}
	// Strip permission flags before parseFlags so they don't trip the
	// "unknown flag" error. Permission flags are handled separately.
	commonAfterPerm, perms, err := parsePermFlags(common)
	if err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	flags, err := parseFlags(commonAfterPerm)
	if err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	// Reject flow/goal-only flags in agent mode.
	if flags.quiet {
		fmt.Fprintln(stderr, "--quiet is not supported for agent command")
		exit(3)
		return
	}
	if flags.summaryOnly {
		fmt.Fprintln(stderr, "--summary-only is not supported for agent command")
		exit(3)
		return
	}
	if flags.resume != "" {
		fmt.Fprintln(stderr, "--resume is not supported for agent command")
		exit(3)
		return
	}
	if flags.showPlan {
		fmt.Fprintln(stderr, "--plan is not supported for agent command")
		exit(3)
		return
	}
	if extra, err := readPipedStdin(); err != nil {
		fmt.Fprintf(stderr, "reading stdin: %v\n", err)
		exit(3)
		return
	} else if extra != "" {
		prompt += "\n\n" + extra
	}
	if err := applyWorkdir(flags.workdir); err != nil {
		fmt.Fprintln(stderr, err)
		exit(3)
		return
	}
	// Use buildOrchestratorOpts for consistent LLM provider wiring.
	opts := buildOrchestratorOpts(flags)
	if maxTurns > 0 {
		opts = append(opts, zenflow.WithMaxTurns(maxTurns))
	}
	if maxDepth > 0 {
		opts = append(opts, zenflow.WithMaxDepth(maxDepth))
	}
	// Wire permission handler (interactive TTY prompts or flag-based pre-approval).
	opts = append(opts, zenflow.WithPermissions(newCliPermissionHandler(perms, osStdin(), stderr, stdinIsTTY())))
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
	var result *zenflow.AgentResult
	runWithWatchdog(flags.timeout, func() {
		result, err = runAgent(orch, ctx, prompt)
	})
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		exit(1)
		return
	}
	// Verbose: agent output already streamed/emitted via OnOutput.
	// Non-verbose: events + narration only, no agent content.
	if flags.verbose && !flags.stream {
		fmt.Fprintln(stdout, result.Content)
	}
}
