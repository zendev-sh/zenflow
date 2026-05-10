// Permission handling for the zenflow CLI binary.
// The standalone `zenflow` CLI runs workflows whose tools (bash, write,
// ...) require user approval. This file wires an interactive permission
// handler that prompts on stdin, plus pre-approval / pre-deny flags for
// non-interactive use (CI, scripts).
// Behavior matrix:
//
//	flag combination | result
//	-------------------------+---------------------------------
//	--yolo | allow every tool, no prompt (YOLO mode)
//	--allow bash,read | allow listed without prompt;
//
// | prompt others (or deny if non-TTY)
//
//	--deny bash,write | deny listed without prompt
//	--strict + --allow ... | deny anything not on --allow list
//	(no flags) + TTY | prompt for every tool call
//	(no flags) + non-TTY | deny with helpful error message
//
// Interactive prompt:
//
//	Tool [bash] wants to run. Allow? [y/N/a (always)]
//
// Responses:
//
//	y / Y allow once
//	n / N / empty deny + report tool error
//	a / A allow this tool name for the rest of the run
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zendev-sh/zenflow"
)

// stdinDeadlineReader is satisfied by *os.File (terminals and pipes on Unix).
// When the concrete stdin reader implements this interface, prompt uses
// SetReadDeadline to interrupt the blocked read on ctx cancellation, which
// prevents the read goroutine from leaking permanently.
// Types that do NOT satisfy this interface (e.g. *strings.Reader, *bytes.Buffer,
// blockingReader used in tests) fall back to the goroutine-with-documented-leak
// pattern - acceptable because those types are only used in tests or non-stdin
// code paths.
type stdinDeadlineReader interface {
	io.Reader
	SetReadDeadline(t time.Time) error
}

// The canonical sandbox safe-tool set lives at zenflow.SandboxDefaultAllow
// (re-exported from internal/exec). The CLI references it directly so the
// library and the CLI cannot drift.

// permFlags holds the parsed permission-related CLI flags. Separate from
// cmdFlags to avoid bloating the shared parseFlags codepath - these flags
// are flow-only.
type permFlags struct {
	yolo    bool     // --yolo: allow everything, skip prompts (YOLO mode)
	allow   []string // --allow tool1,tool2: pre-allowed tool names
	deny    []string // --deny tool1,tool2: pre-denied tool names
	strict  bool     // --strict: deny anything not on --allow list (no prompt)
	sandbox bool     // --sandbox: restrict to safe read/write tools, block bash
}

// hasAny reports whether any permission-related flag is set. Used by the
// caller to decide whether to wire a custom PermissionHandler at all.
func (p permFlags) hasAny() bool {
	return p.yolo || len(p.allow) > 0 || len(p.deny) > 0 || p.strict || p.sandbox
}

// parsePermFlags extracts permission flags from an argv slice and returns
// the remaining args (with permission flags removed) plus the parsed flags.
// This lets cmdFlow strip permission flags before delegating to parseFlags.
// --sandbox semantics (applied after the loop):
// - implies --strict
// - prepends zenflow.SandboxDefaultAllow (read, write, grep, glob)
// - explicitly removes "bash" from the final allow list, even if the caller
// passed --allow bash alongside --sandbox (sandbox wins)
// - mutually exclusive with --yolo (returns error if both are set)
func parsePermFlags(args []string) (remaining []string, pf permFlags, err error) {
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--yolo":
			pf.yolo = true
		case "--strict":
			pf.strict = true
		case "--sandbox":
			pf.sandbox = true
		case "--allow":
			i++
			if i >= len(args) {
				return nil, pf, fmt.Errorf("--allow requires a comma-separated tool list")
			}
			pf.allow = splitCSV(args[i])
		case "--deny":
			i++
			if i >= len(args) {
				return nil, pf, fmt.Errorf("--deny requires a comma-separated tool list")
			}
			pf.deny = splitCSV(args[i])
		default:
			remaining = append(remaining, args[i])
		}
	}
	if pf.yolo && (len(pf.allow) > 0 || len(pf.deny) > 0 || pf.strict) {
		// --yolo auto-approves everything; combining it with --allow/--deny/
		// --strict is a contradiction the runtime would silently let --yolo
		// win (it's checked first in RequestPermission). Catching the
		// ambiguity at parse time prevents a class of "I set --deny and it
		// didn't deny anything" bugs.
		return nil, pf, fmt.Errorf("--yolo auto-approves every tool; remove --allow/--deny/--strict (or drop --yolo)")
	}
	if pf.sandbox {
		if pf.yolo {
			return nil, pf, fmt.Errorf("cannot combine --sandbox and --yolo")
		}
		// Reject overlap between --deny and sandbox defaults - the deny check
		// fires before allow in RequestPermission, so a sandbox-default tool
		// on the deny list is silently denied even though sandbox "enables" it.
		// This ambiguity is almost certainly a user mistake; fail loudly.
		var conflicts []string
		sandboxDefaults := zenflow.SandboxDefaultAllow()
		for _, d := range pf.deny {
			for _, s := range sandboxDefaults {
				if d == s {
					conflicts = append(conflicts, d)
				}
			}
		}
		if len(conflicts) > 0 {
			return nil, pf, fmt.Errorf("--deny conflicts with --sandbox defaults: %v (sandbox enables read/write/grep/glob; remove from --deny or drop --sandbox)", conflicts)
		}
		// --sandbox implies strict mode.
		pf.strict = true
		// Prepend the safe defaults so explicit --allow X,Y can add more
		// on top, but bash is always blocked (filtered out below).
		merged := make([]string, 0, len(sandboxDefaults)+len(pf.allow))
		merged = append(merged, sandboxDefaults...)
		merged = append(merged, pf.allow...)
		// Remove "bash" regardless of what the caller passed - sandbox wins.
		filtered := merged[:0]
		for _, t := range merged {
			if t != "bash" {
				filtered = append(filtered, t)
			}
		}
		pf.allow = filtered
	}
	return remaining, pf, nil
}

// splitCSV splits "a,b,c" into ["a","b","c"], trimming whitespace and
// dropping empty entries. Tolerates trailing commas and stray spaces.
func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cliPermissionHandler implements zenflow.PermissionHandler for the standalone
// CLI binary. It supports flag-based pre-approval/pre-deny, interactive TTY
// prompts, and "always-allow-this-tool" remembering across the run.
// Concurrency: zenflow may call RequestPermission from multiple goroutines
// (parallel steps). The mutex guards the alwaysAllow set and serializes
// prompts so two parallel tool calls don't interleave on stdin.
type cliPermissionHandler struct {
	flags permFlags
	in    io.Reader // stdin (injectable for tests)
	out   io.Writer // prompt output (typically stderr)
	isTTY bool      // false → non-interactive, default deny on unlisted

	mu          sync.Mutex
	alwaysAllow map[string]bool // tool names allowed for the rest of the run
}

// - compile-time assertion. PermissionHandler is documented as
// extensible by library consumers; the CLI is the reference impl, so
// catching signature drift here prevents a downstream user's
// implementation from breaking only at link time.
var _ zenflow.PermissionHandler = (*cliPermissionHandler)(nil)

// newCliPermissionHandler constructs a handler. The caller is responsible for
// passing a stdin reader and detecting TTY status (so tests can override).
func newCliPermissionHandler(flags permFlags, in io.Reader, out io.Writer, isTTY bool) *cliPermissionHandler {
	return &cliPermissionHandler{
		flags:       flags,
		in:          in,
		out:         out,
		isTTY:       isTTY,
		alwaysAllow: make(map[string]bool),
	}
}

// RequestPermission implements zenflow.PermissionHandler. The pure decision
// (yolo / deny / allow / strict / alwaysAllow) is delegated to
// zenflow.DecidePermission; this method owns only the CLI-side concerns:
// snapshotting the alwaysAllow map under the mutex, picking the non-TTY
// error message, and dispatching to the interactive prompt.
// Decision order (enforced by zenflow.DecidePermission):
// 1. --yolo flag → allow.
// 2. Explicit deny list → deny.
// 3. Explicit allow list → allow.
// 4. --strict + not on allow list → deny.
// 5. Already remembered "always" for this tool → allow.
// 6. Otherwise: non-TTY → deny with helpful message; TTY → prompt.
func (h *cliPermissionHandler) RequestPermission(ctx context.Context, req zenflow.PermissionRequest) (bool, error) {
	// Snapshot alwaysAllow under the lock so DecidePermission sees a
	// consistent view; copying is fine because the map is tiny (one entry
	// per tool the user has promoted this run).
	h.mu.Lock()
	always := make(map[string]bool, len(h.alwaysAllow))
	for k, v := range h.alwaysAllow {
		always[k] = v
	}
	h.mu.Unlock()

	policy := zenflow.PermissionPolicy{
		Yolo:   h.flags.yolo,
		Allow:  h.flags.allow,
		Deny:   h.flags.deny,
		Strict: h.flags.strict,
	}
	allowed, prompt, err := zenflow.DecidePermission(policy, req.ToolName, always)
	if err != nil {
		return false, err
	}
	if allowed {
		return true, nil
	}
	// DecidePermission's contract: when allowed=false and err=nil, prompt
	// is always true (see internal/exec/permission_policy.go return-value
	// docs). The previous defensive `if !prompt { ... }` branch was
	// unreachable through that contract and tripped the per-function
	// 100% coverage gate. Contract-honouring callers fall straight through
	// to the TTY check below.
	_ = prompt

	if !h.isTTY {
		return false, fmt.Errorf("tool %q requires permission; pass --yolo or --allow %s on a non-interactive terminal", req.ToolName, req.ToolName)
	}
	return h.prompt(ctx, req)
}

// prompt blocks reading stdin for an interactive y/N/a decision. Serialized
// across parallel tool calls so the user sees one question at a time.
// When h.in satisfies stdinDeadlineReader (i.e. it is an *os.File such as the
// real os.Stdin), ctx cancellation is propagated via SetReadDeadline: a watcher
// goroutine calls SetReadDeadline(time.Now) the moment ctx.Done fires, which
// unblocks the reader goroutine immediately with os.ErrDeadlineExceeded. Both
// goroutines exit cleanly - no goroutine leak.
// When h.in does NOT satisfy stdinDeadlineReader (e.g. test fakes such as
// strings.Reader, bytes.Buffer, blockingReader), the implementation falls back to
// the goroutine+select pattern. If ctx cancels while the goroutine is parked on
// Read, the goroutine remains blocked until the reader returns on its own (e.g.
// the test closes the done channel, or the process exits). This is the same
// behavior as before and is acceptable for non-File readers.
func (h *cliPermissionHandler) prompt(ctx context.Context, req zenflow.PermissionRequest) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Re-check always-allow after grabbing the lock - another goroutine may
	// have just promoted this tool to "always" while we were queued.
	if h.alwaysAllow[req.ToolName] {
		return true, nil
	}

	fmt.Fprintf(h.out, "Tool [%s] wants to run (step=%s). Allow? [y/N/a (always)] ", req.ToolName, req.StepID)

	type readResult struct {
		line string
		err  error
	}

	// Fast path: h.in supports SetReadDeadline (e.g. *os.File). Use a watcher
	// goroutine that sets an immediate deadline on ctx cancellation so the reader
	// goroutine unblocks without leaking.
	if dr, ok := h.in.(stdinDeadlineReader); ok {
		ch := make(chan readResult, 1)
		done := make(chan struct{}) // closed when reader exits

		go func() {
			defer close(done)
			reader := bufio.NewReader(dr)
			line, err := reader.ReadString('\n')
			ch <- readResult{line: line, err: err}
		}()

		// Watcher: set an immediate deadline the moment ctx is done.
		go func() {
			select {
			case <-ctx.Done():
				// Trigger os.ErrDeadlineExceeded in the reader goroutine.
				_ = dr.SetReadDeadline(time.Now())
			case <-done:
				// Reader finished normally - nothing to do.
			}
		}()

		res := <-ch
		// Clear the deadline so subsequent reads on the same fd work normally.
		_ = dr.SetReadDeadline(time.Time{})

		if ctx.Err() != nil {
			return false, fmt.Errorf("permission prompt cancelled: %w", ctx.Err())
		}
		if res.err != nil && res.err != io.EOF {
			return false, fmt.Errorf("read permission response: %w", res.err)
		}
		switch strings.ToLower(strings.TrimSpace(res.line)) {
		case "y", "yes":
			return true, nil
		case "a", "always":
			h.alwaysAllow[req.ToolName] = true
			return true, nil
		default:
			return false, fmt.Errorf("tool %q denied by user", req.ToolName)
		}
	}

	// Fallback: h.in does not support SetReadDeadline. Use the goroutine+select
	// pattern. If ctx cancels, the read goroutine stays parked until the reader
	// returns on its own; process exit reaps it. This path is only reached by
	// non-File readers (test fakes, piped buffers) where the goroutine lifecycle
	// is bounded by the test itself.
	reader := bufio.NewReader(h.in)
	ch := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- readResult{line: line, err: err}
	}()

	var line string
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("permission prompt cancelled: %w", ctx.Err())
	case res := <-ch:
		if res.err != nil && res.err != io.EOF {
			return false, fmt.Errorf("read permission response: %w", res.err)
		}
		line = res.line
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "a", "always":
		h.alwaysAllow[req.ToolName] = true
		return true, nil
	default:
		return false, fmt.Errorf("tool %q denied by user", req.ToolName)
	}
}

// stdinIsTTY reports whether the process's stdin is connected to a terminal.
// Mirrors the existing isTerminal helper in main.go but operates on stdin.
// Injectable for tests.
var stdinIsTTY = func() bool {
	f := osStdin()
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
