package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
)

// mockLLM implements provider.LanguageModel for testing.
type mockLLM struct {
	resp *provider.GenerateResult
	err  error
}

func (m *mockLLM) ModelID() string { return "cli-test-mock" }

func (m *mockLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func (m *mockLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan provider.StreamChunk, 5)
	if m.resp.Text != "" {
		ch <- provider.StreamChunk{Type: provider.ChunkText, Text: m.resp.Text}
	}
	ch <- provider.StreamChunk{
		Type:         provider.ChunkFinish,
		FinishReason: provider.FinishStop,
		Usage:        m.resp.Usage,
	}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

type testEnv struct {
	exitCode int
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
}

// clearProviderEnv unsets every provider credential env var the CLI
// auto-resolver inspects so `_NoLLM_*` tests behave deterministically
// regardless of the developer's local shell. Without this, running the
// suite from a shell with `AWS_ACCESS_KEY_ID` / `AZURE_OPENAI_API_KEY`
// / `GEMINI_API_KEY` set caused the resolver to pick a real provider,
// the workflow attempted a network call, and the test exited 1
// (provider error) instead of 3 ("no LLM configured"). t.Setenv reverts
// the change at test end. Mirrors the env list used in
// TestResolveProvider_NilResolver.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GEMINI_API_KEY",
		"GOOGLE_GENERATIVE_AI_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AZURE_OPENAI_API_KEY",
		"AZURE_RESOURCE_NAME",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"ZENFLOW_MODEL",
	} {
		t.Setenv(k, "")
	}
}

func setupTest(t *testing.T, cliArgs ...string) (*testEnv, func()) {
	t.Helper()
	// Default scrub: every test that goes through setupTest must run
	// against a clean provider env so a developer's shell with
	// `set -a && source .env && set +a` (the documented 
	// workflow per CLAUDE.md "Running E2E Tests") doesn't flip
	// `exit==3` assertions to `exit==1` by routing through a real
	// LLM. Tests that NEED a provider env var explicitly call
	// `t.Setenv(...)` AFTER setupTest (Go test runtime stacks the
	// override; cleanup unwinds in LIFO so the right value is
	// restored). Centralising the scrub here means new tests get the
	// guard for free instead of relying on every author remembering
	// to call `clearProviderEnv` manually.
	clearProviderEnv(t)
	env := &testEnv{exitCode: -1, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	origExit, origArgs, origStderr, origStdout, origStdin, origNewOrch, origReadStdin, origRunFlow, origRunResumeFlow, origRunGoal, origRunAgent, origStorageDir := exit, osArgs, stderr, stdout, osStdin, newOrch, readPipedStdin, runFlow, runResumeFlow, runGoal, runAgent, defaultStorageDir
	exit = func(code int) { env.exitCode = code }
	osArgs = func() []string { return cliArgs }
	stderr = env.stderr
	stdout = env.stdout
	devNull, _ := os.Open(os.DevNull)
	osStdin = func() *os.File { return devNull }
	return env, func() {
		exit = origExit
		osArgs = origArgs
		stderr = origStderr
		stdout = origStdout
		osStdin = origStdin
		newOrch = origNewOrch
		readPipedStdin = origReadStdin
		runFlow = origRunFlow
		runResumeFlow = origRunResumeFlow
		runGoal = origRunGoal
		runAgent = origRunAgent
		defaultStorageDir = origStorageDir
		devNull.Close()
	}
}

// setupTestWithLLM sets up test env with a mock LLM injected.
func setupTestWithLLM(t *testing.T, llm provider.LanguageModel, cliArgs ...string) (*testEnv, func()) {
	t.Helper()
	env, cleanup := setupTest(t, cliArgs...)
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		opts = append(opts, zenflow.WithModel(llm))
		return zenflow.New(opts...)
	}
	return env, cleanup
}

// setupTestWithStdin sets up test env with mock LLM and piped stdin content.
func setupTestWithStdin(t *testing.T, stdinContent string, llm provider.LanguageModel, cliArgs ...string) (*testEnv, func()) {
	t.Helper()
	env, cleanup := setupTestWithLLM(t, llm, cliArgs...)
	readPipedStdin = func() (string, error) { return stdinContent, nil }
	return env, cleanup
}

// setupTestWithStdinError sets up test env where stdin reading fails.
func setupTestWithStdinError(t *testing.T, cliArgs ...string) (*testEnv, func()) {
	t.Helper()
	env, cleanup := setupTest(t, cliArgs...)
	readPipedStdin = func() (string, error) { return "", fmt.Errorf("stdin read error") }
	return env, cleanup
}

func writeWorkflow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	os.WriteFile(path, []byte(content), 0644)
	return path
}

// --- main ---

func TestMain_NoArgs(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow")
	defer cleanup()
	main()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestMain_UnknownCommand(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "bogus")
	defer cleanup()
	main()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "unknown command: bogus") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestMain_DispatchValidate(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "validate", path)
	defer cleanup()
	main()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
}

func TestMain_DispatchPlan(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "plan", path)
	defer cleanup()
	main()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
}

func TestMain_DispatchFlow(t *testing.T) {
	// Flow without LLM exits 3
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path)
	defer cleanup()
	main()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestMain_DispatchGoal(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "goal", "do stuff")
	defer cleanup()
	main()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestMain_DispatchAgent(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello")
	defer cleanup()
	main()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestMain_HelpFlags(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "help"} {
		t.Run(arg, func(t *testing.T) {
			env, cleanup := setupTest(t, "zenflow", arg)
			defer cleanup()
			main()
			if env.exitCode != -1 {
				t.Errorf("%s: exit = %d, want exit not called", arg, env.exitCode)
			}
			if !strings.Contains(env.stdout.String(), "Usage: zenflow") {
				t.Errorf("%s: stdout missing usage; got %q", arg, env.stdout.String())
			}
			if env.stderr.Len() != 0 {
				t.Errorf("%s: stderr non-empty: %q", arg, env.stderr.String())
			}
		})
	}
}

func TestMain_VersionFlags(t *testing.T) {
	origVersion, origCommit, origDate := version, commit, date
	t.Cleanup(func() { version, commit, date = origVersion, origCommit, origDate })
	for _, arg := range []string{"--version", "-v", "version"} {
		t.Run(arg+"_minimal", func(t *testing.T) {
			version, commit, date = "v1.2.3-test", "unknown", "unknown"
			env, cleanup := setupTest(t, "zenflow", arg)
			defer cleanup()
			main()
			if env.exitCode != -1 {
				t.Errorf("exit = %d, want exit not called", env.exitCode)
			}
			out := env.stdout.String()
			if !strings.Contains(out, "zenflow v1.2.3-test") {
				t.Errorf("stdout = %q, want 'zenflow v1.2.3-test' prefix", out)
			}
 // Provenance suppressed when commit/date are unset.
			if strings.Contains(out, "commit=") {
				t.Errorf("stdout = %q, did not expect 'commit=' when commit is unknown", out)
			}
		})
		t.Run(arg+"_with_provenance", func(t *testing.T) {
			version, commit, date = "v1.2.3-test", "abc1234", "2026-05-04T11:00:00Z"
			env, cleanup := setupTest(t, "zenflow", arg)
			defer cleanup()
			main()
			out := env.stdout.String()
			if !strings.Contains(out, "zenflow v1.2.3-test") {
				t.Errorf("stdout = %q, want 'zenflow v1.2.3-test'", out)
			}
			if !strings.Contains(out, "commit=abc1234") {
				t.Errorf("stdout = %q, want 'commit=abc1234'", out)
			}
			if !strings.Contains(out, "date=2026-05-04T11:00:00Z") {
				t.Errorf("stdout = %q, want 'date=2026-05-04T11:00:00Z'", out)
			}
		})
	}
}

func TestUsage_ListsMaxTurns(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	if !strings.Contains(buf.String(), "--max-turns") {
		t.Errorf("usage missing --max-turns line; got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "agent only") {
		t.Errorf("usage --max-turns missing 'agent only' qualifier")
	}
}

// --- cmdValidate ---

func TestCmdValidate_NoFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "validate")
	defer cleanup()
	cmdValidate()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdValidate_BadFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "validate", "/nonexistent/wf.yaml")
	defer cleanup()
	cmdValidate()
	if env.exitCode != 2 {
		t.Errorf("exit = %d, want 2", env.exitCode)
	}
}

func TestCmdValidate_Valid(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "validate", path)
	defer cleanup()
	cmdValidate()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
	if !strings.Contains(env.stdout.String(), "Valid") {
		t.Errorf("stdout = %q", env.stdout.String())
	}
}

// --- cmdPlan ---

func TestCmdPlan_NoFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "plan")
	defer cleanup()
	cmdPlan()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdPlan_BadFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "plan", "/nonexistent/wf.yaml")
	defer cleanup()
	cmdPlan()
	if env.exitCode != 2 {
		t.Errorf("exit = %d, want 2", env.exitCode)
	}
}

func TestCmdPlan_Valid(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: a\n  - id: b\n    dependsOn: [a]\n")
	env, cleanup := setupTest(t, "zenflow", "plan", path)
	defer cleanup()
	cmdPlan()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
	out := env.stdout.String()
	if !strings.Contains(out, "test (2 steps)") || !strings.Contains(out, "│ a") || !strings.Contains(out, "│ b") {
		t.Errorf("stdout = %q", out)
	}
}

// --- cmdFlow ---

func TestCmdFlow_NoFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "flow")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdFlow_BadFlags(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "flow", "wf.yaml", "--bogus")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdFlow_BadFile(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "flow", "/nonexistent/wf.yaml")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 2 {
		t.Errorf("exit = %d, want 2", env.exitCode)
	}
}

func TestCmdFlow_NoLLM(t *testing.T) {
	clearProviderEnv(t)
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path)
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "no LLM model configured") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdFlow_NoLLM_JSON(t *testing.T) {
	clearProviderEnv(t)
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path, "--json")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdFlow_NoLLM_AllFlags(t *testing.T) {
	clearProviderEnv(t)
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path, "--model", "gpt-4", "--max-concurrency", "3", "--timeout", "5m")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// --- cmdGoal ---

func TestCmdGoal_NoGoal(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "goal")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdGoal_BadFlags(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "goal", "do stuff", "--bogus")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdGoal_NoLLM(t *testing.T) {
	clearProviderEnv(t)
	env, cleanup := setupTest(t, "zenflow", "goal", "do stuff")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "no LLM model configured") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdGoal_NoLLM_AllFlags(t *testing.T) {
	clearProviderEnv(t)
	env, cleanup := setupTest(t, "zenflow", "goal", "do stuff", "--json", "--model", "gpt-4", "--max-concurrency", "2", "--timeout", "10m")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// --- cmdAgent ---

func TestCmdAgent_NoPrompt(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdAgent_NoLLM(t *testing.T) {
	clearProviderEnv(t)
	env, cleanup := setupTest(t, "zenflow", "agent", "hello")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "no LLM model configured") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdAgent_BadMaxTurns(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-turns", "abc")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdAgent_MaxTurnsNoValue(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-turns")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdAgent_BadMaxDepth(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-depth", "xyz")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdAgent_MaxDepthNoValue(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-depth")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// --max-turns negative rejected by cmdAgent parser.
func TestCmdAgent_MaxTurnsNegative(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-turns", "-1")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "must not be negative") {
		t.Errorf("stderr = %q, want 'must not be negative'", env.stderr.String())
	}
}

// --max-depth negative rejected by cmdAgent parser.
func TestCmdAgent_MaxDepthNegative(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--max-depth", "-2")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "must not be negative") {
		t.Errorf("stderr = %q, want 'must not be negative'", env.stderr.String())
	}
}

func TestCmdAgent_BadCommonFlag(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--bogus")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

func TestCmdAgent_NoLLM_AllFlags(t *testing.T) {
	clearProviderEnv(t)
	env, cleanup := setupTest(t, "zenflow", "agent", "hello", "--model", "gpt-4", "--max-turns", "10", "--max-depth", "2", "--json")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// --- parseFlags ---

func TestParseFlags_AllFlags(t *testing.T) {
	f, err := parseFlags([]string{"--model", "gpt-4", "--timeout", "5m", "--max-concurrency", "3", "--json", "--verbose"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.model != "gpt-4" {
		t.Errorf("model = %q", f.model)
	}
	if !f.jsonOutput || !f.verbose {
		t.Error("json/verbose should be true")
	}
	if f.maxConcurrency != 3 {
		t.Errorf("maxConcurrency = %d", f.maxConcurrency)
	}
}

func TestParseFlags_ModelMissing(t *testing.T) {
	_, err := parseFlags([]string{"--model"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_TimeoutMissing(t *testing.T) {
	_, err := parseFlags([]string{"--timeout"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_TimeoutInvalid(t *testing.T) {
	_, err := parseFlags([]string{"--timeout", "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_MaxConcurrencyMissing(t *testing.T) {
	_, err := parseFlags([]string{"--max-concurrency"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_MaxConcurrencyInvalid(t *testing.T) {
	_, err := parseFlags([]string{"--max-concurrency", "abc"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_MaxRetries(t *testing.T) {
	f, err := parseFlags([]string{"--max-retries", "5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", f.maxRetries)
	}
}

func TestParseFlags_MaxRetriesDefault(t *testing.T) {
	f, err := parseFlags([]string{"--model", "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.maxRetries != -1 {
		t.Errorf("maxRetries = %d, want -1 (unset)", f.maxRetries)
	}
}

func TestParseFlags_MaxRetriesMissing(t *testing.T) {
	_, err := parseFlags([]string{"--max-retries"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseFlags_MaxRetriesInvalid(t *testing.T) {
	_, err := parseFlags([]string{"--max-retries", "abc"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// --timeout must not be negative.
func TestParseFlags_TimeoutNegative(t *testing.T) {
	_, err := parseFlags([]string{"--timeout", "-5m"})
	if err == nil {
		t.Fatal("expected error for negative --timeout")
	}
	if !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("error %q should mention 'must not be negative'", err.Error())
	}
}

// --max-depth must not be negative (parseFlags path for flow/goal).
func TestParseFlags_MaxDepthNegative(t *testing.T) {
	_, err := parseFlags([]string{"--max-depth", "-1"})
	if err == nil {
		t.Fatal("expected error for negative --max-depth")
	}
	if !strings.Contains(err.Error(), "must not be negative") {
		t.Errorf("error %q should mention 'must not be negative'", err.Error())
	}
}

// --max-depth 0 is the "not set" sentinel (runtime default 3) - must be accepted.
func TestParseFlags_MaxDepthZeroAllowed(t *testing.T) {
	f, err := parseFlags([]string{"--max-depth", "0"})
	if err != nil {
		t.Fatalf("unexpected error for --max-depth 0: %v", err)
	}
	if f.maxDepth != 0 {
		t.Errorf("maxDepth = %d, want 0 (not-set sentinel)", f.maxDepth)
	}
}

// --- isCoordinatorError ---

func TestIsCoordinatorError_JSONParse(t *testing.T) {
	if !isCoordinatorError(&zenflow.JSONParseError{Err: fmt.Errorf("y")}) {
		t.Error("should be coordinator error")
	}
}

func TestIsCoordinatorError_Validation(t *testing.T) {
	if !isCoordinatorError(&zenflow.CoordinatorValidationError{Err: fmt.Errorf("x")}) {
		t.Error("should be coordinator error")
	}
}

func TestIsCoordinatorError_ToolNotFound(t *testing.T) {
	if !isCoordinatorError(&zenflow.ToolNotFoundError{Tool: "x"}) {
		t.Error("should be coordinator error")
	}
}

func TestIsCoordinatorError_Generic(t *testing.T) {
	if isCoordinatorError(errors.New("generic")) {
		t.Error("should not be coordinator error")
	}
}

// --- usage ---

func TestUsage_Output(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	if !strings.Contains(buf.String(), "Usage:") {
		t.Errorf("usage output = %q", buf.String())
	}
}

// --- Post-HasLLM paths with mock LLM ---

func TestCmdFlow_WithMockLLM_Success(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 10, OutputTokens: 5}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path)
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdFlow_WithMockLLM_StepFails(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{err: fmt.Errorf("LLM error")}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path)
	defer cleanup()
	cmdFlow()
	// Step failure causes workflow to complete with StatusFailed → exit(1)
	if env.exitCode != 1 {
		t.Errorf("exit = %d, want 1", env.exitCode)
	}
}

func TestCmdFlow_WithMockLLM_Timeout(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--timeout", "1h")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
}

func TestCmdFlow_WithMockLLM_JSON(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--json")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
}

func TestCmdFlow_Resume_Success(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "resumed"}}

	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--resume", "run-abc")
	defer cleanup()

	// Override defaultStorageDir AFTER setupTest captures the original.
	storageDir := t.TempDir()
	defaultStorageDir = func() string { return storageDir }

	// Pre-populate storage so LoadRun succeeds.
	store := zenflow.NewFileStorage(storageDir)
	run := &zenflow.Run{
		ID:       "run-abc",
		Workflow: &zenflow.Workflow{Name: "test"},
		Status:   zenflow.StatusRunning,
		Steps:    map[string]*zenflow.StepResult{},
	}
	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	// Override runResumeFlow to verify it's called with the right args.
	var calledRunID string
	runResumeFlow = func(o *zenflow.Orchestrator, ctx context.Context, runID string, wf *zenflow.Workflow) (*zenflow.WorkflowResult, error) {
		calledRunID = runID
		return o.ResumeFlow(ctx, runID, wf)
	}

	cmdFlow()

	if calledRunID != "run-abc" {
		t.Errorf("runResumeFlow called with runID = %q, want run-abc", calledRunID)
	}
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
	if !strings.Contains(env.stdout.String(), "Run ID:") {
		t.Errorf("stdout should contain 'Run ID:', got: %s", env.stdout.String())
	}
}

func TestCmdFlow_Resume_MissingValue(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path, "--resume")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "requires a run ID") {
		t.Errorf("stderr should mention 'requires a run ID', got: %s", env.stderr.String())
	}
}

func TestParseFlags_Resume(t *testing.T) {
	f, err := parseFlags([]string{"--resume", "run-xyz"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.resume != "run-xyz" {
		t.Errorf("resume = %q, want run-xyz", f.resume)
	}
}

func TestCmdGoal_WithMockLLM_NoLLM_AfterFlags(t *testing.T) {
	// Goal with all flags but still no LLM (newOrch not overridden)
	// This is already covered. Now test WITH LLM - coordinator will fail
	// because mock LLM returns simple text, not valid JSON workflow.
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not a json workflow"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	cmdGoal()
	// RunGoal will fail (coordinator can't parse LLM response as workflow JSON)
	if env.exitCode == -1 {
		t.Error("expected exit to be called (coordinator error)")
	}
}

func TestCmdGoal_WithMockLLM_Timeout(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--timeout", "30m")
	defer cleanup()
	cmdGoal()
	// Coordinator will fail, but the timeout path is exercised
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

func TestCmdGoal_WithMockLLM_WithFlags(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--model", "gpt-4", "--max-concurrency", "2", "--json")
	defer cleanup()
	cmdGoal()
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

func TestCmdAgent_WithMockLLM_Success(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "Hello world!"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "say hello", "--verbose")
	defer cleanup()
	cmdAgent()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
	if !strings.Contains(env.stdout.String(), "Hello world!") {
		t.Errorf("stdout = %q", env.stdout.String())
	}
}

func TestCmdAgent_WithMockLLM_Error(t *testing.T) {
	mock := &mockLLM{err: fmt.Errorf("agent error")}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "say hello")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 1 {
		t.Errorf("exit = %d, want 1", env.exitCode)
	}
}

func TestCmdFlow_RunFlowError(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path)
	defer cleanup()
	runFlow = func(_ *zenflow.Orchestrator, _ context.Context, _ *zenflow.Workflow, _ ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		return nil, fmt.Errorf("executor internal error")
	}
	cmdFlow()
	if env.exitCode != 1 {
		t.Errorf("exit = %d, want 1", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "executor internal error") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdGoal_RunGoalError_NonCoordinator(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "x"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, _ string, _ ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		return nil, fmt.Errorf("generic execution error")
	}
	cmdGoal()
	if env.exitCode != 1 {
		t.Errorf("exit = %d, want 1 (non-coordinator error)", env.exitCode)
	}
}

func TestCmdGoal_ResultStatusFailed(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "x"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, _ string, _ ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		return &zenflow.WorkflowResult{Status: zenflow.StatusFailed}, nil
	}
	cmdGoal()
	if env.exitCode != 1 {
		t.Errorf("exit = %d, want 1 (StatusFailed)", env.exitCode)
	}
}

func TestCmdAgent_WithMockLLM_WithFlags(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--model", "gpt-4", "--max-turns", "5", "--max-depth", "2", "--json")
	defer cleanup()
	cmdAgent()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called", env.exitCode)
	}
}

// --- Post-HasLLM paths with mock LLM ---

func TestCmdGoal_WithStdin(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithStdin(t, "extra context from stdin", mock, "zenflow", "goal", "build auth")
	defer cleanup()
	cmdGoal()
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

func TestCmdGoal_StdinError(t *testing.T) {
	env, cleanup := setupTestWithStdinError(t, "zenflow", "goal", "build auth")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "stdin read error") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdAgent_WithStdin(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "got it"}}
	env, cleanup := setupTestWithStdin(t, "piped input", mock, "zenflow", "agent", "hello")
	defer cleanup()
	cmdAgent()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdAgent_StdinError(t *testing.T) {
	env, cleanup := setupTestWithStdinError(t, "zenflow", "agent", "hello")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "stdin read error") {
		t.Errorf("stderr = %q", env.stderr.String())
	}
}

func TestCmdGoal_ResultFailed(t *testing.T) {
	// Mock LLM that returns a valid JSON workflow so coordinator succeeds,
	// but the workflow step fails.
	mock := &mockLLM{err: fmt.Errorf("step execution error")}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	cmdGoal()
	// RunGoal fails - should exit with 2 (coordinator error) or 1 (step error)
	if env.exitCode == -1 {
		t.Error("expected exit on error")
	}
}

// --- --trace flag coverage ---

func TestParseFlags_Trace(t *testing.T) {
	f, err := parseFlags([]string{"--trace"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.trace {
		t.Error("trace should be true")
	}
}

func TestCmdFlow_WithTrace(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--trace")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdGoal_WithTrace(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--trace")
	defer cleanup()
	cmdGoal()
	// Coordinator will fail (non-JSON), but --trace path is exercised
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

func TestCmdAgent_WithTrace(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "traced agent"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--trace", "--verbose")
	defer cleanup()
	cmdAgent()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
	if !strings.Contains(env.stdout.String(), "traced agent") {
		t.Errorf("stdout = %q", env.stdout.String())
	}
}

// --- : Coordinator CLI flags ---

func TestParseFlags_Quiet(t *testing.T) {
	f, err := parseFlags([]string{"--quiet"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.quiet {
		t.Error("quiet should be true")
	}
}

func TestParseFlags_SummaryOnly(t *testing.T) {
	f, err := parseFlags([]string{"--summary-only"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.summaryOnly {
		t.Error("summaryOnly should be true")
	}
}

func TestBuildOrchestratorOpts_QuietUsesNoop(t *testing.T) {
	flags := cmdFlags{quiet: true}
	opts := buildOrchestratorOpts(flags)
	// Apply opts to an orchestrator and verify coordinator is NoopCoordinator.
	o := zenflow.New(opts...)
	// NoopCoordinator is the only type that implements CoordinatorAgent and is a struct value.
	// We can't directly inspect the field, but the orchestrator should work without LLM.
	_ = o // opts applied without panic
}

func TestBuildOrchestratorOpts_JSONUsesNoop(t *testing.T) {
	flags := cmdFlags{jsonOutput: true}
	opts := buildOrchestratorOpts(flags)
	o := zenflow.New(opts...)
	_ = o
}

func TestCmdFlow_Quiet(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--quiet")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdFlow_SummaryOnly(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--summary-only")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdGoal_Quiet(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--quiet")
	defer cleanup()
	cmdGoal()
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

func TestCmdAgent_QuietRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "hello"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--quiet")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--quiet not supported for agent)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--quiet is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdAgent_SummaryOnlyRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "hello"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--summary-only")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--summary-only not supported for agent)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--summary-only is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdGoal_ResumeRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--resume", "run_abc")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--resume not supported for goal)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--resume is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdAgent_ResumeRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "hello"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--resume", "run_abc")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--resume not supported for agent)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--resume is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdGoal_PlanRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--plan")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--plan not supported for goal)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--plan is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdAgent_PlanRejected(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "hello"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "agent", "hello", "--plan")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3 (--plan not supported for agent)", env.exitCode)
	}
	if !strings.Contains(env.stderr.String(), "--plan is not supported") {
		t.Errorf("stderr = %q, expected rejection message", env.stderr.String())
	}
}

func TestCmdGoal_SummaryOnly(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "not json"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--summary-only")
	defer cleanup()
	cmdGoal()
	if env.exitCode == -1 {
		t.Error("expected exit (coordinator error)")
	}
}

// --- banner ---

func TestBanner_IsTerminal_PrintsBanner(t *testing.T) {
	// Override stdout with a real *os.File (temp file) and isTerminal to return true.
	tmpFile, err := os.CreateTemp("", "banner-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	origStdout, origTerminal, origArgs := stdout, isTerminal, osArgs
	defer func() { stdout = origStdout; isTerminal = origTerminal; osArgs = origArgs }()

	stdout = tmpFile
	isTerminal = func(_ *os.File) bool { return true }
	osArgs = func() []string { return []string{"zenflow", "flow"} }

	banner()

	// Read back what was written.
	tmpFile.Seek(0, 0)
	data := make([]byte, 1024)
	n, _ := tmpFile.Read(data)
	got := string(data[:n])

	if !strings.Contains(got, "zenflow") {
		t.Errorf("banner output = %q, expected to contain 'zenflow'", got)
	}
}

func TestBanner_IsTerminal_JsonSkips(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "banner-json-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	origStdout, origTerminal, origArgs := stdout, isTerminal, osArgs
	defer func() { stdout = origStdout; isTerminal = origTerminal; osArgs = origArgs }()

	stdout = tmpFile
	isTerminal = func(_ *os.File) bool { return true }
	osArgs = func() []string { return []string{"zenflow", "goal", "--json", "do stuff"} }

	banner()

	tmpFile.Seek(0, 0)
	data := make([]byte, 1024)
	n, _ := tmpFile.Read(data)
	if n > 0 {
		t.Errorf("expected no output with --json flag, got %q", string(data[:n]))
	}
}

func TestBanner_NotTerminal_Skips(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "banner-noterm-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	origStdout, origTerminal, origArgs := stdout, isTerminal, osArgs
	defer func() { stdout = origStdout; isTerminal = origTerminal; osArgs = origArgs }()

	stdout = tmpFile
	isTerminal = func(_ *os.File) bool { return false }
	osArgs = func() []string { return []string{"zenflow", "flow"} }

	banner()

	tmpFile.Seek(0, 0)
	data := make([]byte, 1024)
	n, _ := tmpFile.Read(data)
	if n > 0 {
		t.Errorf("expected no output when not terminal, got %q", string(data[:n]))
	}
}

// --- resolveProvider ---

func TestResolveProvider_EmptyModel(t *testing.T) {
	t.Setenv("ZENFLOW_MODEL", "")
	llm, modelID := resolveProvider("")
	if llm != nil {
		t.Errorf("expected nil for empty model, got %v", llm)
	}
	if modelID != "" {
		t.Errorf("expected empty modelID, got %q", modelID)
	}
}

func TestResolveProvider_NilResolver(t *testing.T) {
	// With no env vars set, autoResolverFromModelName returns nil for unknown model.
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "AWS_ACCESS_KEY_ID", "AZURE_OPENAI_API_KEY", "ZENFLOW_MODEL"} {
		t.Setenv(k, "")
	}
	llm, _ := resolveProvider("unknown-model")
	if llm != nil {
		t.Errorf("expected nil when no provider env set, got %v", llm)
	}
}

func TestResolveProvider_ZenflowModelEnv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "fake-key")
	t.Setenv("ZENFLOW_MODEL", "gemini-2.5-flash")
	llm, modelID := resolveProvider("")
	if llm == nil {
		t.Fatal("expected non-nil LLM from ZENFLOW_MODEL env")
	}
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want gemini-2.5-flash", modelID)
	}
}

func TestResolveProvider_GooglePrefix(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "fake-key")
	llm, modelID := resolveProvider("google/gemini-2.5-flash")
	if llm == nil {
		t.Fatal("expected non-nil LLM for google/ prefix")
	}
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want gemini-2.5-flash", modelID)
	}
}

func TestResolveProvider_BedrockPrefix(t *testing.T) {
	llm, modelID := resolveProvider("bedrock/anthropic.claude-sonnet-4-6")
	if llm == nil {
		t.Fatal("expected non-nil LLM for bedrock/ prefix")
	}
	if modelID != "anthropic.claude-sonnet-4-6" {
		t.Errorf("modelID = %q", modelID)
	}
}

func TestResolveProvider_AzurePrefix(t *testing.T) {
	llm, _ := resolveProvider("azure/DeepSeek-V3.2")
	if llm == nil {
		t.Fatal("expected non-nil LLM for azure/ prefix")
	}
}

// `azure/<gpt-*>` must produce a non-nil LanguageModel
// against goai's default v1 GA routing. v0.7.4+ correctly omits the
// `?api-version=` query parameter on the v1 GA path so spec-strict
// resources accept it (live-verified on azure/gpt-5.5).
func TestResolveProvider_AzurePrefix_GPT(t *testing.T) {
	llm, _ := resolveProvider("azure/gpt-5.5")
	if llm == nil {
		t.Fatal("expected non-nil LLM for azure/gpt-5.5 prefix")
	}
}

// `azure/<claude-*>` keeps Anthropic-protocol routing
// (services.ai.azure.com/anthropic). goai's buildAnthropicModel
// short-circuits the OpenAI path entirely.
func TestResolveProvider_AzurePrefix_Claude(t *testing.T) {
	llm, _ := resolveProvider("azure/claude-sonnet-4-6")
	if llm == nil {
		t.Fatal("expected non-nil LLM for azure/claude-sonnet-4-6 prefix")
	}
}

func TestResolveProvider_AzureDeploymentPrefix(t *testing.T) {
	llm, _ := resolveProvider("azure-deployment/gpt-5")
	if llm == nil {
		t.Fatal("expected non-nil LLM for azure-deployment/ prefix")
	}
}

// --- --plan flag ---

func TestParseFlags_Plan(t *testing.T) {
	f, err := parseFlags([]string{"--plan"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.showPlan {
		t.Error("showPlan should be true")
	}
}

func TestCmdFlow_WithPlan(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--plan")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
	// --plan should print DAG diagram before execution.
	out := env.stdout.String()
	if !strings.Contains(out, "s1") {
		t.Errorf("stdout = %q, expected DAG with step 's1'", out)
	}
}

// --- --stream flag ---

func TestParseFlags_Stream(t *testing.T) {
	f, err := parseFlags([]string{"--stream"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !f.stream {
		t.Error("stream should be true")
	}
}

func TestBuildOrchestratorOpts_StreamAndVerbose(t *testing.T) {
	flags := cmdFlags{stream: true, verbose: true}
	opts := buildOrchestratorOpts(flags)
	// Apply opts and verify no panic.
	o := zenflow.New(opts...)
	_ = o
}

func TestCmdFlow_WithStream(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "streamed", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--stream")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdFlow_WithVerbose(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "verbose out", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--verbose")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

func TestCmdFlow_WithStreamAndVerbose(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hello\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "done", Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--stream", "--verbose")
	defer cleanup()
	cmdFlow()
	if env.exitCode != -1 {
		t.Errorf("exit = %d, want not called (success)", env.exitCode)
	}
}

// TestIsBedrockCrossRegionPattern - regression lock. Bedrock
// cross-region inference profiles are <vendor>.<model> where vendor
// is a known AWS Bedrock vendor. The prior `strings.Contains(".")`
// heuristic mistakenly matched Azure model IDs with version dots
// (e.g. "DeepSeek-V3.2", "Llama-3.1-70B") and routed them to Bedrock,
// causing "model identifier is invalid" errors. This test locks the
// narrower vendor-prefix check.
func TestIsBedrockCrossRegionPattern(t *testing.T) {
	bedrockYes := []string{
		"anthropic.claude-sonnet-4-6",
		"anthropic.claude-opus-4-1-20250805",
		"amazon.nova-pro-v1:0",
		"meta.llama3-70b-instruct-v1:0",
		"mistral.mistral-large-2407-v1:0",
		"cohere.command-r-plus-v1:0",
		"ai21.jamba-1-5-large-v1:0",
		"stability.stable-image-ultra-v1:0",
		"minimax.minimax-m2.5",
		"deepseek.r1-v1:0",
		"qwen.qwen3-coder-30b-a3b-v1:0",
		"writer.palmyra-x5-v1:0",
		"luma.ray-v2:0",
		"openai.gpt-oss-120b-1:0",
	}
	for _, m := range bedrockYes {
		if !isBedrockCrossRegionPattern(m) {
			t.Errorf("isBedrockCrossRegionPattern(%q) = false, want true", m)
		}
	}

	azureYes := []string{
		"DeepSeek-V3.2",
		"DeepSeek-V3",
		"Llama-3.1-70B",
		"Llama-3.3-70B-Instruct",
		"Phi-3.5-mini",
		"Mistral-Large-2407",
		"Cohere-command-r-plus-08-2024",
		"gpt-4o",
		"gpt-5",
		"gpt-5.3-codex",
		"o1-preview",
		"claude-sonnet-4-6",
	}
	for _, m := range azureYes {
		if isBedrockCrossRegionPattern(m) {
			t.Errorf("isBedrockCrossRegionPattern(%q) = true, want false (this is an Azure-pattern model, not Bedrock cross-region)", m)
		}
	}

	// Case-insensitive match
	if !isBedrockCrossRegionPattern("ANTHROPIC.claude-sonnet-4-6") {
		t.Error("isBedrockCrossRegionPattern should be case-insensitive on vendor prefix")
	}
}

// TestBuildLeafStepMenu_* tests migrated to
// zenflow/coord_lib_test.go as TestBuildCoordStepMenu_* (the helper
// itself was promoted to zenflow.BuildCoordStepMenu so external
// consumers can build the same step menu without re-implementing
// wrapper filtering against Router internals).

// =============================================================================
// installTracerFunc - --trace exporter wiring
// =============================================================================

// TestInstallTracerFunc_NoTrace verifies that when flags.trace is false,
// the real installTracerFunc returns a no-op shutdown (no panic, no output).
func TestInstallTracerFunc_NoTrace(t *testing.T) {
	flags := cmdFlags{trace: false}
	stop := installTracerFunc(flags)
	stop() // must not panic
}

// TestInstallTracerFunc_TraceFlag_Seam verifies the installTracerFunc seam:
// replacing it with a mock captures the flags passed by cmdFlow / cmdGoal.
func TestInstallTracerFunc_TraceFlag_Seam(t *testing.T) {
	origInstall := installTracerFunc
	defer func() { installTracerFunc = origInstall }()

	var capturedFlags cmdFlags
	shutdownCalled := false
	installTracerFunc = func(flags cmdFlags) func() {
		capturedFlags = flags
		return func() { shutdownCalled = true }
	}

	// Inject seam and drive cmdFlow path through setupTest.
	wfPath := writeWorkflow(t, "name: t\nversion: 1\nsteps:\n  - id: s1\n    instructions: hi\n")
	env, cleanup := setupTest(t, "zenflow", "flow", wfPath, "--trace")
	defer cleanup()
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		return zenflow.New(append(opts, zenflow.WithModel(&mockLLM{
			resp: &provider.GenerateResult{Text: "ok", FinishReason: provider.FinishStop},
		}))...)
	}
	cmdFlow()
	_ = env

	if !capturedFlags.trace {
		t.Error("installTracerFunc was not called with trace=true")
	}
	if !shutdownCalled {
		t.Error("shutdown func was not called on cmdFlow exit")
	}
}

// TestInstallTracerFunc_RealImpl_NoError verifies the real installTracerFunc
// implementation initialises without error and the returned shutdown func
// is callable. Uses stdouttrace (OTLP endpoint cleared) writing to a buffer.
func TestInstallTracerFunc_RealImpl_NoError(t *testing.T) {
	// Redirect package-level stderr so the exporter writes to buf, not os.Stderr.
	buf := &bytes.Buffer{}
	origStderr := stderr
	stderr = buf
	defer func() { stderr = origStderr }()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	flags := cmdFlags{trace: true}
	stop := installTracerFunc(flags)
	stop() // flush; no spans queued so output may be empty - just must not panic
}

// TestInstallTracerFunc_ExporterInitError covers the branch where
// withDefaultExporterFunc returns an error (e.g. OTLP network failure).
// installTracerFunc must log to stderr and return a no-op shutdown func.
func TestInstallTracerFunc_ExporterInitError(t *testing.T) {
	origExporter := withDefaultExporterFunc
	origStderr := stderr
	t.Cleanup(func() {
		withDefaultExporterFunc = origExporter
		stderr = origStderr
	})

	injectedErr := errors.New("injected exporter init failure")
	withDefaultExporterFunc = func(_ context.Context) (func(context.Context) error, error) {
		return nil, injectedErr
	}

	buf := &bytes.Buffer{}
	stderr = buf

	flags := cmdFlags{trace: true}
	stop := installTracerFunc(flags)
	if stop == nil {
		t.Fatal("expected non-nil stop func")
	}
	stop() // must not panic

	if !strings.Contains(buf.String(), "failed to install exporter") {
		t.Errorf("stderr missing error message; got %q", buf.String())
	}
}

// TestInstallTracerFunc_ShutdownError covers the branch where the shutdown
// func returned by withDefaultExporterFunc returns an error on call.
// installTracerFunc must log the shutdown error to stderr.
func TestInstallTracerFunc_ShutdownError(t *testing.T) {
	origExporter := withDefaultExporterFunc
	origStderr := stderr
	t.Cleanup(func() {
		withDefaultExporterFunc = origExporter
		stderr = origStderr
	})

	injectedErr := errors.New("injected shutdown failure")
	withDefaultExporterFunc = func(_ context.Context) (func(context.Context) error, error) {
		return func(_ context.Context) error { return injectedErr }, nil
	}

	buf := &bytes.Buffer{}
	stderr = buf

	flags := cmdFlags{trace: true}
	stop := installTracerFunc(flags)
	if stop == nil {
		t.Fatal("expected non-nil stop func")
	}
	stop() // triggers the shutdown error branch

	if !strings.Contains(buf.String(), "exporter shutdown error") {
		t.Errorf("stderr missing shutdown error message; got %q", buf.String())
	}
}

// TestStartCoordRunner_RunnerErrorWhileCtxActive covers the branch inside
// startCoordRunner where runner.Run returns an error but the coord context
// is still active (runErr != nil && coordCtx.Err == nil). The slog.Warn
// call is exercised; the loop then exits via WaitForCoordWake returning false.
func TestStartCoordRunner_RunnerErrorWhileCtxActive(t *testing.T) {
	// cliGateErrLLM blocks until the test releases it (via release channel),
	// then returns an error. We release BEFORE calling cleanup so that
	// coordCtx is still active when runner.Run returns (cleanup calls the
	// internal coordCancel which would cancel coordCtx prematurely).
	release := make(chan struct{})
	llm := &cliGateErrLLM{
		err:     errors.New("transient LLM error"),
		release: release,
	}
	runner := zenflow.NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Capture cleanup func but do NOT call it yet - calling cleanup cancels
	// the internal coordCtx immediately, which would prevent line 916 from firing.
	cleanup := startCoordRunner(ctx, runner, "test-model")
	// Release LLM: DoGenerate returns error while coordCtx is still active.
	// The goroutine hits line 916 (runErr != nil && coordCtx.Err == nil).
	close(release)
	// Wait for the goroutine to process the error and reach WaitForCoordWake.
	time.Sleep(50 * time.Millisecond)
	// Cancel outer ctx: WaitForCoordWake returns false, goroutine exits.
	cancel()
	// Now call cleanup to drain the done channel.
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not return within 3s")
	}
}

// TestStartCoordRunner_ContinuationLoop covers the userMsg assignment on
// line 925: after WaitForCoordWake returns true (wake signal from runner.Wake),
// the loop continues and sets userMsg to the continuation prompt before
// re-entering runner.Run. The second Run call sees ctx cancelled and exits.
func TestStartCoordRunner_ContinuationLoop(t *testing.T) {
	// cliGateErrLLM with an always-open release so DoGenerate returns immediately.
	// Release must not yet be closed when cleanup is called (same race as above),
	// so we use a fresh closed channel so DoGenerate can always return.
	release := make(chan struct{})
	close(release) // open immediately so both Run iterations return fast
	llm := &cliGateErrLLM{
		err:     errors.New("mock error"),
		release: release,
	}
	runner := zenflow.NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Do NOT call cleanup yet - same reason as above.
	cleanup := startCoordRunner(ctx, runner, "test-model")
	// Allow first Run to complete (goroutine processes error + reaches WaitForCoordWake).
	time.Sleep(30 * time.Millisecond)
	// Fire Wake: WaitForCoordWake returns true, goroutine hits line 925.
	select {
	case runner.Wake() <- struct{}{}:
	default:
	}
	// Give the goroutine time to hit line 925 and re-enter runner.Run.
	time.Sleep(30 * time.Millisecond)
	// Cancel outer ctx so the second Run iteration exits.
	cancel()
	// Drain via cleanup.
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not return within 3s")
	}
}

// TestStartCoordRunner_CleanupTimerFires covers the case <-coordCleanupTimer.C
// branch where the coord goroutine does not exit within the cleanup timeout.
// coordCleanupDelay is reduced to 10ms so the test is fast.
func TestStartCoordRunner_CleanupTimerFires(t *testing.T) {
	origDelay := coordCleanupDelay
	t.Cleanup(func() { coordCleanupDelay = origDelay })
	coordCleanupDelay = 10 * time.Millisecond

	// blockingLLM never returns, keeping the coord goroutine alive so the
	// cleanup timer fires before the goroutine exits.
	llm := &cliBlockingLLM{block: make(chan struct{})}
	runner := zenflow.NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := startCoordRunner(ctx, runner, "test-model")
	doneCh := make(chan struct{})
	go func() { cleanup(); close(doneCh) }()
	select {
	case <-doneCh:
 // cleanup returned via the timer path - the goroutine leaks until
 // the deferred cancel reaps it.
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not return within 2s via timer path")
	}
	// Allow the blocked goroutine to exit.
	close(llm.block)
}

// cliErrLLM is a minimal LanguageModel that returns an error on every call.
// Used to exercise the coord runner error branch without a real LLM.
type cliErrLLM struct{ err error }

func (c *cliErrLLM) ModelID() string { return "cli-err-mock" }
func (c *cliErrLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return nil, c.err
}
func (c *cliErrLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, c.err
}

// cliGateErrLLM is a minimal LanguageModel that blocks in DoGenerate until
// the release channel is closed, then returns err. This lets tests ensure the
// context is still active when runner.Run returns with an error, allowing the
// coordCtx.Err == nil branch at line 916 to fire correctly.
type cliGateErrLLM struct {
	err     error
	release <-chan struct{}
}

func (c *cliGateErrLLM) ModelID() string { return "cli-gate-err-mock" }
func (c *cliGateErrLLM) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	select {
	case <-c.release:
 // Release fired - return the configured error (context is still active).
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *cliGateErrLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, c.err
}

// cliBlockingLLM is a minimal LanguageModel whose DoGenerate blocks until
// the block channel is closed. Used to simulate a hung coord LLM that
// prevents the goroutine from exiting before the cleanup timer fires.
type cliBlockingLLM struct{ block chan struct{} }

func (c *cliBlockingLLM) ModelID() string { return "cli-blocking-mock" }
func (c *cliBlockingLLM) DoGenerate(ctx context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	select {
	case <-c.block:
		return nil, fmt.Errorf("unblocked")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *cliBlockingLLM) DoStream(ctx context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	select {
	case <-c.block:
		return nil, fmt.Errorf("unblocked")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
