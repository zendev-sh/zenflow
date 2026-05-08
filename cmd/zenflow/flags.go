package main

// flags.go - common CLI flag parsing for the flow / goal / agent
// subcommands plus the small helpers (`isHelpArg`, `argsContainHelp`,
// `splitPositionalContext`) that route help vs positional context vs
// flag tokens. Permission-related flags live in permission.go;
// agent-specific flags (`--max-turns`, `--max-depth`) are still
// parsed inline in cmdAgent because they predate parseFlags and are
// stripped from the common arg slice before this parser runs.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cmdFlags holds common CLI flags shared across flow, goal, and agent commands.
type cmdFlags struct {
	model          string
	timeout        time.Duration
	maxConcurrency int
	maxRetries     int // -1 = use goai default, 0+ = override
	maxDepth       int // 0 = use runtime default (3), >0 = override
	jsonOutput     bool
	verbose        bool
	quiet          bool   // suppress coordinator narration, show events only
	summaryOnly    bool   // skip per-step narration, show summary at end
	resume         string // run ID to resume from (flow command only)
	trace          bool   // enable OTel tracing (flow/goal commands)
	stream         bool   // enable streaming output
	showPlan       bool   // show DAG diagram before execution
	workdir        string // working directory for LLM tool execution (sandbox)
	thinking       string // reasoning/thinking level: off|low|medium|high (default off)
}

// parseFlags parses common CLI flags from args (starting after the positional argument).
// It returns the parsed flags and an error if an unknown flag or missing value is encountered.
func parseFlags(args []string) (cmdFlags, error) {
	f := cmdFlags{maxRetries: -1}
	// First pass: normalise `--key=value` into separate `--key` `value`
	// tokens and respect a `--` POSIX terminator (everything after `--`
	// is rejected with a clear message rather than silently consumed).
	// Note: bare positional tokens (no `-` prefix) are NOT rejected
	// here because flag VALUES (e.g. `30s` after `--timeout`) also
	// lack a `-` prefix; the switch below handles them in pairs.
	// The default branch below catches truly stray positionals with
	// a friendlier message than the previous "unknown flag: <word>".
	normalised := make([]string, 0, len(args))
	terminatorSeen := false
	for _, a := range args {
		if a == "--" {
			terminatorSeen = true
			continue
		}
		if terminatorSeen {
			return f, fmt.Errorf("zenflow does not accept positional arguments after `--` (got %q)", a)
		}
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
			normalised = append(normalised, k, v)
			continue
		}
		normalised = append(normalised, a)
	}
	args = normalised
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--model requires a value")
			}
			f.model = args[i]
		case "--timeout":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--timeout requires a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return f, fmt.Errorf("invalid timeout: %w", err)
			}
			if d < 0 {
				return f, fmt.Errorf("--timeout must not be negative: %q", args[i])
			}
			f.timeout = d
		case "--max-concurrency":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--max-concurrency requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("invalid max-concurrency: %w", err)
			}
			if n < 1 {
				return f, fmt.Errorf("invalid max-concurrency %d: must be >= 1", n)
			}
			f.maxConcurrency = n
		case "--max-retries":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--max-retries requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("invalid max-retries: %w", err)
			}
			if n < 0 {
				return f, fmt.Errorf("invalid max-retries %d: must be >= 0", n)
			}
			f.maxRetries = n
		case "--max-depth":
 // Cap recursion depth for nested agent spawning. cmdAgent
 // has its own --max-depth parser (predates parseFlags);
 // flow/goal route it through here via WithMaxDepth(flags.
 // maxDepth) in buildOrchestratorOpts.
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--max-depth requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("invalid max-depth: %w", err)
			}
			if n < 0 {
				return f, fmt.Errorf("--max-depth must not be negative: %d", n)
			}
			f.maxDepth = n
		case "--json":
			f.jsonOutput = true
		case "--verbose":
			f.verbose = true
		case "--resume":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--resume requires a run ID")
			}
			f.resume = args[i]
		case "--trace":
			f.trace = true
		case "--stream":
			f.stream = true
		case "--plan":
			f.showPlan = true
		case "--quiet":
			f.quiet = true
		case "--summary-only":
			f.summaryOnly = true
		case "--workdir":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--workdir requires a value")
			}
			f.workdir = args[i]
		case "--thinking":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--thinking requires a value (off|low|medium|high)")
			}
			switch args[i] {
			case "off", "low", "medium", "high":
				f.thinking = args[i]
			default:
				return f, fmt.Errorf("invalid --thinking value %q (want off|low|medium|high)", args[i])
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				return f, fmt.Errorf("unexpected positional argument %q (subcommands accept at most one positional; rest must be flags)", args[i])
			}
			return f, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return f, nil
}

// isHelpArg reports whether arg is a help-flag form (`--help`, `-h`, or
// `help`). Used by every subcommand handler to dispatch to the global
// `usage(stdout)` when the user types `zenflow flow --help` etc. - the
// previous form of those handlers consumed `--help` as the YAML path
// or the goal/agent prompt and emitted misleading errors.
func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
}

// argsContainHelp reports whether any arg in args is a help form. Used
// by subcommand handlers to honour `zenflow flow file.yaml --help` (help
// after the positional) in addition to `zenflow flow --help` (help as
// the positional).
func argsContainHelp(args []string) bool {
	for _, a := range args {
		if isHelpArg(a) {
			return true
		}
	}
	return false
}

// splitPositionalContext splits the args trailing the
// command's first positional (file path or goal text) into:
// - context: the optional 2nd positional, or "" when args[0] starts
// with "--" (i.e. flags begin immediately).
// - rest: the remaining args, suitable to hand to parseFlags.
// Examples:
//	splitPositionalContext([]string{"topic: AI", "--model", "x"})
// → "topic: AI", []string{"--model", "x"}
//	splitPositionalContext([]string{"--quiet"})
// → "", []string{"--quiet"}
//	splitPositionalContext([]string{})
// → "", []string{}
// Restricted to a single positional after the command's primary
// argument - multiple positionals are an unsupported pattern and the
// caller's parseFlags will reject anything that isn't a known flag.
// Both `--<long>` and `-<short>` are recognised as flags so a typo like
// `zenflow flow file.yaml -v` does not silently get swallowed as
// positional context (single-dash short flags are uncommon at the
// subcommand level today, but treating them as flags keeps the
// splitter consistent with the parser's "anything starting with `-`
// is a flag" rule).
func splitPositionalContext(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	if strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}
