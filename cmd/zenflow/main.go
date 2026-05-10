// Command zenflow runs multi-agent workflows from YAML definitions.
package main

// main.go - thin entrypoint plus the package-level test seams the CLI
// exposes (stdout/stderr writers, osArgs, exit, readPipedStdin,
// newOrch, runFlow / runResumeFlow / runGoal / runAgent, version
// strings, defaultStorageDir). The actual subcommand handlers and
// flag plumbing live in their own files (cmd_*.go, flags.go,
// workdir.go, provider.go, thinking.go, orchestrator_opts.go,
// permission.go, signals.go, trace_otel.go).

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/zendev-sh/zenflow"
)

// watchdogGrace is the extra time allowed after flags.timeout before the
// CLI force-exits with code 124. Gives ctx cancellation a reasonable window
// to propagate through goai → provider HTTP before we declare the process hung.
// var (not const) so tests can shorten it to exercise the hard-timeout path
// without waiting 30 seconds.
var watchdogGrace = 30 * time.Second

// runWithWatchdog runs fn and, if flags.timeout is set, enforces a hard
// deadline of (timeout + watchdogGrace). If fn does not return within that
// window, the CLI dumps all goroutine stacks to stderr and force-exits with
// code 124 (GNU timeout(1) convention).
// This is a last-resort safety net: when a provider's DoGenerate
// ignores ctx cancellation, the normal ctx.Done propagation path can hang
// indefinitely. The watchdog guarantees the user never experiences the
// "41 minutes past --timeout" symptom observed in E2E testing.
// Orphaned goroutines are killed when exit runs (os.Exit terminates the
// whole process). In tests where exit is a no-op fake, the watchdog path
// is still exercised but the process stays alive.
func runWithWatchdog(timeout time.Duration, fn func()) {
	if timeout <= 0 {
		fn()
		return
	}
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	hardDeadline := time.NewTimer(timeout + watchdogGrace)
	defer hardDeadline.Stop()
	select {
	case <-done:
		return
	case <-hardDeadline.C:
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		fmt.Fprintf(stderr, "zenflow: hard timeout exceeded (%s + %s grace). Goroutine dump:\n%s\n", timeout, watchdogGrace, buf[:n])
		exit(124)
	}
}

// defaultStorageDir returns ~/.zenflow/runs/ for file-based persistence.
// Injectable test seam wrapping the public zenflow.DefaultStorageDir.
var defaultStorageDir = zenflow.DefaultStorageDir

// version, commit, and date carry release-time build provenance.
// Overridden via -ldflags by goreleaser / the OSS Dockerfile. goreleaser
// strips the leading `v` from `{{.Version}}`, so the binary reports e.g.
// `0.1.0` for tag `v0.1.0`. To reproduce that locally:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always | sed 's/^v//') \
//
// -X main.commit=$(git rev-parse --short HEAD) \
// -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
// ./cmd/zenflow
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// readBuildInfo is injectable for testing. Production uses debug.ReadBuildInfo.
var readBuildInfo = debug.ReadBuildInfo

// Injected for testing. Production uses os.Exit/os.Args/os.Stderr/os.Stdin/os.Stdout.
var (
	exit              = os.Exit
	osArgs            = func() []string { return os.Args }
	stderr  io.Writer = os.Stderr
	stdout  io.Writer = os.Stdout
	osStdin           = func() *os.File { return os.Stdin }
	// readPipedStdin reads piped stdin content. Returns empty string if stdin is a terminal.
	// The `err != nil` branch after io.ReadAll is structurally unreachable in
	// normal operation: osStdin is called twice (once for Stat, once for
	// ReadAll) and returns the same *os.File both times. A file that reports
	// non-terminal via Stat will not fail ReadAll unless the OS returns an
	// I/O error mid-read - which cannot be reproduced without kernel-level
	// injection. The branch is kept for robustness but is untestable without
	// refactoring the closure to call osStdin only once and pass the *os.File
	// to both calls. Coverage gap is intentional; do not remove the branch.
	readPipedStdin = func() (string, error) {
		stat, _ := osStdin().Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(osStdin())
			if err != nil { // coverage-note: kernel I/O error on stdin; untestable without OS-level injection
				return "", err
			}
			return string(data), nil
		}
		return "", nil
	}
	// newOrch creates the orchestrator. Injectable for testing with mock LLM.
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator { return zenflow.New(opts...) }
	// runFlow/runGoal/runAgent wrap orchestrator methods. Injectable for testing error paths.
	// runFlow/runGoal accept variadic per-call options
	// (RunFlowOption / RunGoalOption) so cmdFlow / cmdGoal can pass
	// WithFlowContext / WithGoalContext from the optional 2nd
	// positional argument. Tests can swap the seam to capture the
	// passed options.
	runFlow = func(o *zenflow.Orchestrator, ctx context.Context, wf *zenflow.Workflow, opts ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		return o.RunFlow(ctx, wf, opts...)
	}
	runResumeFlow = func(o *zenflow.Orchestrator, ctx context.Context, runID string, wf *zenflow.Workflow) (*zenflow.WorkflowResult, error) {
		return o.ResumeFlow(ctx, runID, wf)
	}
	runGoal = func(o *zenflow.Orchestrator, ctx context.Context, goal string, opts ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		return o.RunGoal(ctx, goal, opts...)
	}
	runAgent = func(o *zenflow.Orchestrator, ctx context.Context, prompt string) (*zenflow.AgentResult, error) {
		return o.RunAgent(ctx, zenflow.AgentConfig{Prompt: prompt})
	}
)

// banner prints the zenflow header. Skipped when stdout is not a terminal
// (piped / redirected) or when --json flag is present.
func banner() {
	// Skip banner if stdout is not a terminal.
	if f, ok := stdout.(*os.File); !ok || !isTerminal(f) {
		return
	}
	// Skip banner if --json flag is present anywhere in args.
	for _, arg := range osArgs() {
		if arg == "--json" {
			return
		}
	}
	fmt.Fprintln(stdout, C(Cyan, "≋≋≋ zenflow - let agents flow ≋≋≋"))
	fmt.Fprintln(stdout)
}

// isTerminal reports whether the given file is a terminal.
var isTerminal = func(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func main() {
	a := osArgs()
	if len(a) < 2 {
		usage(stderr)
		exit(3)
		return
	}
	switch a[1] {
	case "--help", "-h", "help":
		usage(stdout)
		return
	case "--version", "-v", "version":
		// Format: "zenflow <version> (commit=<hash>, date=<iso8601>)"
		// - keeping the leading "zenflow" lets brew test grep for the
		// binary name and the parenthesised provenance is suppressed
		// for the default unset values so `--version` still prints
		// just "zenflow dev" in unbuilt local runs. Both commit AND
		// date must be set for the parenthesised form; goreleaser
		// always sets both, so a half-set state is a build
		// misconfiguration that falls back to the minimal form rather
		// than printing "date=unknown" or "commit=unknown".
		// When ldflags are at their defaults ("dev" / "unknown"), fall
		// back to runtime/debug.ReadBuildInfo for VCS provenance so
		// `go install`-built binaries still report useful version info.
		v, c, d := version, commit, date
		if v == "dev" {
			if info, ok := readBuildInfo(); ok {
				if info.Main.Version != "" && info.Main.Version != "(devel)" {
					v = info.Main.Version
				}
				for _, s := range info.Settings {
					switch s.Key {
					case "vcs.revision":
						if len(s.Value) >= 7 {
							c = s.Value[:7]
						} else if s.Value != "" {
							c = s.Value
						}
					case "vcs.time":
						if s.Value != "" {
							d = s.Value
						}
					}
				}
			}
		}
		if c != "unknown" && d != "unknown" {
			fmt.Fprintf(stdout, "zenflow %s (commit=%s, date=%s)\n", v, c, d)
		} else {
			fmt.Fprintf(stdout, "zenflow %s\n", v)
		}
		return
	}
	banner()
	switch a[1] {
	case "validate":
		cmdValidate()
	case "plan":
		cmdPlan()
	case "flow":
		cmdFlow()
	case "goal":
		cmdGoal()
	case "agent":
		cmdAgent()
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", a[1])
		usage(stderr)
		exit(3)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: zenflow <command> [options]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  validate <file>   Validate a workflow YAML file")
	fmt.Fprintln(w, "  plan <file>       Show execution plan (topological order)")
	fmt.Fprintln(w, "  flow <file>       Execute a workflow")
	fmt.Fprintln(w, "  goal <goal>       Decompose goal into workflow via LLM, then execute (accepts piped stdin)")
	fmt.Fprintln(w, "  agent <prompt>    Run a single-agent conversation (accepts piped stdin)")
	fmt.Fprintln(w, "  help              Print this help text")
	fmt.Fprintln(w, "  version           Print zenflow version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Common flags:")
	fmt.Fprintln(w, "  --model MODEL       LLM model name")
	fmt.Fprintln(w, "  --timeout DURATION  Workflow timeout (applies to all subcommands)")
	fmt.Fprintln(w, "  --max-concurrency N Max parallel steps (flow/goal only)")
	fmt.Fprintln(w, "  --max-depth N       Max nested agent-spawn depth (default: 3)")
	fmt.Fprintln(w, "  --max-retries N     Retry count on transient LLM errors (default: goai default)")
	fmt.Fprintln(w, "  --max-turns N       Conversation turn cap (agent only)")
	fmt.Fprintln(w, "  --verbose           Show agent thinking, tool output content, per-turn token summary")
	fmt.Fprintln(w, "  --stream            Stream agent text token-by-token (combine with --verbose to also stream thinking)")
	fmt.Fprintln(w, "  --plan              Show DAG diagram before execution (flow only)")
	fmt.Fprintln(w, "  --json              NDJSON output (one JSON event per line; includes coord narration. Combine with --quiet to skip coord)")
	fmt.Fprintln(w, "  --quiet             Events only - no narration, no agent output")
	fmt.Fprintln(w, "  --summary-only      Skip per-step narration, show summary at end (flow/goal only)")
	fmt.Fprintln(w, "  --resume RUN_ID     Resume from checkpoint (flow only)")
	fmt.Fprintln(w, "  --workdir DIR       Sandbox directory for tool execution (chdirs here; defaults to cwd)")
	fmt.Fprintln(w, "  --trace             Enable OTel tracing (binaries shipped via Homebrew/GoReleaser have OTel built in; for `go install` from source, rebuild with -tags otel; default: stderr exporter; honors OTEL_EXPORTER_OTLP_ENDPOINT)")
	fmt.Fprintln(w, "  --thinking LEVEL    Extended reasoning: off|low|medium|high (default off)")
	fmt.Fprintln(w, "  --help, -h          Print this help text")
	fmt.Fprintln(w, "  --version, -v       Print zenflow version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Permission flags (flow / goal / agent):")
	fmt.Fprintln(w, "  --yolo              Auto-approve all permission requests (YOLO mode)")
	fmt.Fprintln(w, "  --allow LIST        Comma-separated allow-list (e.g., bash,read)")
	fmt.Fprintln(w, "  --deny LIST         Comma-separated deny-list")
	fmt.Fprintln(w, "  --strict            Reject any tool not explicitly allowed")
	fmt.Fprintln(w, "  --sandbox           Restrict tools to read/write/grep/glob (no shell). Implies --strict.")
	fmt.Fprintln(w, "                      Mutually exclusive with --yolo.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Exit codes: 0 success, 1 step failure, 2 validation error, 3 config error")
}
