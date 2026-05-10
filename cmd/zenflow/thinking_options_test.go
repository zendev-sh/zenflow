package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// thinkingProviderOptions - every branch
// =============================================================================

// TestThinkingProviderOptions_OffEmpty covers the early-return branch:
// "" and "off" both return nil.
func TestThinkingProviderOptions_OffEmpty(t *testing.T) {
	if got := thinkingProviderOptions(""); got != nil {
		t.Errorf("empty: got %v, want nil", got)
	}
	if got := thinkingProviderOptions("off"); got != nil {
		t.Errorf("off: got %v, want nil", got)
	}
}

// TestThinkingProviderOptions_InvalidLevel covers the default-case branch
// (unknown level → nil).
func TestThinkingProviderOptions_InvalidLevel(t *testing.T) {
	if got := thinkingProviderOptions("bogus"); got != nil {
		t.Errorf("bogus: got %v, want nil", got)
	}
}

// TestThinkingProviderOptions_LowMediumHigh covers the three valid levels
// and asserts the produced map contains every provider's reasoning keys
// with the expected budget value.
func TestThinkingProviderOptions_LowMediumHigh(t *testing.T) {
	cases := []struct {
		level  string
		budget int
	}{
		{"low", 1024},
		{"medium", 4096},
		{"high", 16384},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			po := thinkingProviderOptions(tc.level)
			if po == nil {
				t.Fatalf("level %q: got nil", tc.level)
			}
			// Bedrock: reasoningConfig.{type,budgetTokens,maxReasoningEffort}
			rc, ok := po["reasoningConfig"].(map[string]any)
			if !ok {
				t.Fatalf("missing reasoningConfig: %#v", po)
			}
			if rc["type"] != "enabled" {
				t.Errorf("reasoningConfig.type = %v", rc["type"])
			}
			if rc["budgetTokens"] != tc.budget {
				t.Errorf("reasoningConfig.budgetTokens = %v, want %d", rc["budgetTokens"], tc.budget)
			}
			if rc["maxReasoningEffort"] != tc.level {
				t.Errorf("reasoningConfig.maxReasoningEffort = %v, want %s", rc["maxReasoningEffort"], tc.level)
			}
			// Anthropic: thinking.{type,budgetTokens}
			th, ok := po["thinking"].(map[string]any)
			if !ok {
				t.Fatalf("missing thinking: %#v", po)
			}
			if th["budgetTokens"] != tc.budget {
				t.Errorf("thinking.budgetTokens = %v, want %d", th["budgetTokens"], tc.budget)
			}
			// Google: thinkingConfig.{includeThoughts,thinkingLevel,thinkingBudget}
			tc2, ok := po["thinkingConfig"].(map[string]any)
			if !ok {
				t.Fatalf("missing thinkingConfig: %#v", po)
			}
			if tc2["thinkingLevel"] != tc.level {
				t.Errorf("thinkingConfig.thinkingLevel = %v, want %s", tc2["thinkingLevel"], tc.level)
			}
			if tc2["thinkingBudget"] != tc.budget {
				t.Errorf("thinkingConfig.thinkingBudget = %v, want %d", tc2["thinkingBudget"], tc.budget)
			}
			// OpenAI: top-level reasoning_effort + reasoning_summary
			if po["reasoning_effort"] != tc.level {
				t.Errorf("reasoning_effort = %v, want %s", po["reasoning_effort"], tc.level)
			}
			if po["reasoning_summary"] != "auto" {
				t.Errorf("reasoning_summary = %v, want auto", po["reasoning_summary"])
			}
		})
	}
}

// =============================================================================
// parseFlags - --thinking branch
// =============================================================================

// TestParseFlags_Thinking covers parseFlags' --thinking branch for each
// valid level plus the missing-value and invalid-value error paths.
func TestParseFlags_Thinking_Valid(t *testing.T) {
	for _, lvl := range []string{"off", "low", "medium", "high"} {
		t.Run(lvl, func(t *testing.T) {
			f, err := parseFlags([]string{"--thinking", lvl})
			if err != nil {
				t.Fatalf("--thinking %s: %v", lvl, err)
			}
			if f.thinking != lvl {
				t.Errorf("thinking = %q, want %q", f.thinking, lvl)
			}
		})
	}
}

func TestParseFlags_Thinking_MissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--thinking"})
	if err == nil {
		t.Fatal("expected error for missing --thinking value")
	}
	if !strings.Contains(err.Error(), "--thinking requires a value") {
		t.Errorf("err = %v, want missing-value message", err)
	}
}

func TestParseFlags_Thinking_InvalidValue(t *testing.T) {
	_, err := parseFlags([]string{"--thinking", "bogus"})
	if err == nil {
		t.Fatal("expected error for invalid --thinking value")
	}
	if !strings.Contains(err.Error(), "invalid --thinking") {
		t.Errorf("err = %v, want invalid-value message", err)
	}
}

// =============================================================================
// buildOrchestratorOpts - remaining branches
// =============================================================================

// TestBuildOrchestratorOpts_WithThinking covers the `if po :=
// thinkingProviderOptions(...); po != nil` branch in buildOrchestratorOpts.
// Asserts the option slice grows by exactly one when --thinking is set
// (vs. baseline thinking=""), proving the WithGoAIOptions branch executed.
func TestBuildOrchestratorOpts_WithThinking(t *testing.T) {
	baseline := buildOrchestratorOpts(cmdFlags{thinking: ""})
	f := cmdFlags{thinking: "low"}
	opts := buildOrchestratorOpts(f)
	if len(opts) != len(baseline)+1 {
		t.Errorf("len(opts) = %d, baseline = %d; expected baseline+1 (one WithGoAIOptions for thinking)", len(opts), len(baseline))
	}
	orch := zenflowNew(opts...)
	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}
}

// TestBuildOrchestratorOpts_WithMaxRetries covers the `flags.maxRetries >= 0`
// branch.
// Note: the baseline cmdFlags{} has maxRetries=0 which already satisfies
// `>= 0`, so the option is added in BOTH paths. We instead pin behavior
// by asserting that explicit (>=0) values keep the same slice length and
// that the orchestrator accepts the option without panic. To prove the
// branch is exercised, we compare with an explicit maxRetries=-1 (the
// only value that skips the option, per the source).
func TestBuildOrchestratorOpts_WithMaxRetries(t *testing.T) {
	skipped := buildOrchestratorOpts(cmdFlags{maxRetries: -1})
	f := cmdFlags{maxRetries: 7}
	opts := buildOrchestratorOpts(f)
	if len(opts) != len(skipped)+1 {
		t.Errorf("len(opts) = %d, skipped (maxRetries=-1) = %d; expected skipped+1 (one WithGoAIOptions for maxRetries)", len(opts), len(skipped))
	}
	orch := zenflowNew(opts...)
	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}
}

// TestBuildOrchestratorOpts_WithWorkdir covers the `flags.workdir != ""`
// branch (filepathAbs success path) inside buildOrchestratorOpts.
// The workdir branch always appends WithTools (when set OR not - the
// fallback uses cwd), so the slice length doesn't differentiate. Instead
// pin behavior by intercepting filepathAbs and asserting it was called
// with the requested workdir path.
func TestBuildOrchestratorOpts_WithWorkdir(t *testing.T) {
	dir := t.TempDir()
	orig := filepathAbs
	t.Cleanup(func() { filepathAbs = orig })
	var seen string
	filepathAbs = func(p string) (string, error) {
		seen = p
		return orig(p)
	}
	f := cmdFlags{workdir: dir}
	opts := buildOrchestratorOpts(f)
	if seen != dir {
		t.Errorf("filepathAbs called with %q, want %q (workdir branch did not execute)", seen, dir)
	}
	orch := zenflowNew(opts...)
	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}
}

// TestBuildOrchestratorOpts_WithWorkdirAbsErr covers the `filepathAbs`
// error branch - flags.workdir is set but filepath.Abs fails. workdirAbs
// stays empty; buildOrchestratorOpts must not panic. Asserts the
// injected stub was called (proving the err branch was reached).
func TestBuildOrchestratorOpts_WithWorkdirAbsErr(t *testing.T) {
	orig := filepathAbs
	t.Cleanup(func() { filepathAbs = orig })
	var called int
	filepathAbs = func(_ string) (string, error) {
		called++
		return "", errors.New("injected abs failure")
	}
	f := cmdFlags{workdir: "/some/path"}
	opts := buildOrchestratorOpts(f)
	if called == 0 {
		t.Error("filepathAbs stub was never called - workdir branch did not execute")
	}
	orch := zenflowNew(opts...)
	if orch == nil {
		t.Fatal("expected non-nil orchestrator even with abs failure")
	}
}

// =============================================================================
// permission.go prompt - extra branches
// =============================================================================

// TestPrompt_AlwaysAllowReChecked covers permission.go prompt line 176-178
// (the re-check after grabbing the lock - alwaysAllow already set).
// We pre-seed the alwaysAllow map so when prompt runs, the re-check
// short-circuits to allow without consuming stdin.
func TestPrompt_AlwaysAllowReChecked(t *testing.T) {
	h := newCliPermissionHandler(permFlags{}, strings.NewReader(""), &bytes.Buffer{}, true)
	// Pre-seed alwaysAllow under lock to simulate a concurrent promotion
	// happening between RequestPermission's first check and prompt's
	// second check.
	h.mu.Lock()
	h.alwaysAllow["bash"] = true
	h.mu.Unlock()

	ok, err := h.prompt(t.Context(), makeReq("bash"))
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if !ok {
		t.Fatal("expected allow=true after pre-seeded always")
	}
}

// errReader returns a non-EOF error on Read so prompt's
// `err != nil && err != io.EOF` branch fires.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) {
	return 0, errors.New("synthetic read failure")
}

// blockingReader blocks on every Read until ctx-cancel test reaches
// completion. Used to exercise the ctx-aware prompt path: the
// goroutine sits parked on Read while the main path observes
// ctx.Done and returns.
type blockingReader struct{ done chan struct{} }

func (b blockingReader) Read(_ []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

// TestPrompt_CtxCancel covers the ctx-aware prompt branch.
// Before the fix, RequestPermission ignored ctx and prompt blocked
// indefinitely on stdin. Now ctx cancellation surfaces as a wrapped
// "permission prompt cancelled" error.
func TestPrompt_CtxCancel(t *testing.T) {
	doneCh := make(chan struct{})
	defer close(doneCh)
	h := newCliPermissionHandler(permFlags{}, blockingReader{done: doneCh}, &bytes.Buffer{}, true)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	ok, err := h.prompt(ctx, makeReq("bash"))
	if ok {
		t.Fatal("expected deny on ctx cancel")
	}
	if err == nil || !strings.Contains(err.Error(), "permission prompt cancelled") {
		t.Errorf("err = %v, want 'permission prompt cancelled' wrap", err)
	}
}

// TestPrompt_ReadStringNonEOFError covers prompt line 183-185 - reader
// returns a non-EOF error, prompt wraps it and returns deny.
func TestPrompt_ReadStringNonEOFError(t *testing.T) {
	h := newCliPermissionHandler(permFlags{}, errReader{}, &bytes.Buffer{}, true)
	ok, err := h.prompt(t.Context(), makeReq("bash"))
	if ok {
		t.Fatal("expected deny on read error")
	}
	if err == nil || !strings.Contains(err.Error(), "read permission response") {
		t.Errorf("err = %v, want read-permission-response wrap", err)
	}
}

// =============================================================================
// stdinIsTTY - both branches (nil osStdin, Stat error)
// =============================================================================

// TestStdinIsTTY_NilStdin covers the `f == nil` branch (osStdin returns nil).
func TestStdinIsTTY_NilStdin(t *testing.T) {
	prev := osStdin
	t.Cleanup(func() { osStdin = prev })
	osStdin = func() *os.File { return nil }
	if stdinIsTTY() {
		t.Error("expected false when osStdin returns nil")
	}
}

// TestStdinIsTTY_StatErr covers the `Stat != nil` branch by pointing
// osStdin at a closed file (Stat on a closed os.File returns an error).
func TestStdinIsTTY_StatErr(t *testing.T) {
	prev := osStdin
	t.Cleanup(func() { osStdin = prev })

	f, err := os.CreateTemp(t.TempDir(), "zfstdin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	_ = f.Close() // closed file → Stat returns "file already closed"
	osStdin = func() *os.File { return f }
	if stdinIsTTY() {
		t.Error("expected false when Stat fails")
	}
}

// zenflowNew is an alias to the package-level newOrch shim - buildOrchestratorOpts
// returns []zenflow.Option, and we round-trip through newOrch so we can exercise
// the option list without importing zenflow into this test file.
var zenflowNew = newOrch
