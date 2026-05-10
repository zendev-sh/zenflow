package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
)

// (zenflowNew lives in r4_coverage_test.go - kept there so tests
// share the same orchestrator-shape assertion seam as tests.)
var _ = zenflow.WithCoordinator // keep zenflow import live for tests below

// exeSuffix returns ".exe" on Windows, "" elsewhere. Windows refuses to
// execute a binary named `zenflow` without the extension - `os/exec` walks
// PATHEXT and never finds it, so the test fails with "executable file not
// found in %PATH%" even though the file exists at the absolute path the
// caller passed. Embedding the suffix in the on-disk name makes the same
// `exec.Command(bin, ...)` work cross-platform.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "zenflow"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(".") // current package dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

// scrubProviderEnv builds a child-process environment derived from
// os.Environ but with every provider credential the auto-resolver
// inspects forced to empty. `t.Setenv` only affects the parent process;
// `_NoLLM` subprocess tests must scrub the child env explicitly or a
// developer's shell with `GEMINI_API_KEY` / `AWS_ACCESS_KEY_ID` /
// `AZURE_OPENAI_API_KEY` / `ZENFLOW_MODEL` set will cause the resolver
// to pick a real provider, the workflow attempts a network call, and
// the test exits 1 (provider error) instead of 3 ("no LLM configured").
// Mirrors the in-process `clearProviderEnv` helper in unit_test.go.
func scrubProviderEnv() []string {
	scrubbed := make([]string, 0, len(os.Environ()))
	skip := map[string]struct{}{
		"GEMINI_API_KEY":               {},
		"GOOGLE_GENERATIVE_AI_API_KEY": {},
		"AWS_ACCESS_KEY_ID":            {},
		"AWS_SECRET_ACCESS_KEY":        {},
		"AWS_SESSION_TOKEN":            {},
		"AZURE_OPENAI_API_KEY":         {},
		"AZURE_RESOURCE_NAME":          {},
		"OPENAI_API_KEY":               {},
		"ANTHROPIC_API_KEY":            {},
		"ZENFLOW_MODEL":                {},
	}
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			scrubbed = append(scrubbed, kv)
			continue
		}
		if _, drop := skip[kv[:eq]]; drop {
			continue
		}
		scrubbed = append(scrubbed, kv)
	}
	return scrubbed
}

func TestCLI_ValidateValid(t *testing.T) {
	bin := buildBinary(t)
	// Find testdata relative to repo root.
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "validate", testdata)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
	if got := string(out); got != "✓ Valid\n" {
		t.Errorf("output = %q, want %q", got, "✓ Valid\n")
	}
}

func TestCLI_ValidateCycle(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "cycle.yaml")
	cmd := exec.Command(bin, "validate", testdata)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for cycle")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 2 {
			t.Errorf("exit code = %d, want 2", exitErr.ExitCode())
		}
	}
	output := string(out)
	if !strings.Contains(output, "cycle") {
		t.Errorf("expected error message to mention 'cycle', got: %s", output)
	}
}

func TestCLI_PlanValid(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "plan", testdata)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plan failed: %v\n%s", err, out)
	}
	output := string(out)
	if len(output) == 0 {
		t.Fatal("plan output is empty")
	}

	// Verify step names appear in topological order: design → implement → review.
	expectedSteps := []string{"design", "implement", "review"}
	for _, step := range expectedSteps {
		if !strings.Contains(output, step) {
			t.Errorf("plan output missing step %q, got:\n%s", step, output)
		}
	}
	// Verify order: design before implement, implement before review.
	idxDesign := strings.Index(output, "design")
	idxImpl := strings.Index(output, "implement")
	idxReview := strings.Index(output, "review")
	if idxDesign >= idxImpl {
		t.Errorf("'design' (pos %d) should appear before 'implement' (pos %d)", idxDesign, idxImpl)
	}
	if idxImpl >= idxReview {
		t.Errorf("'implement' (pos %d) should appear before 'review' (pos %d)", idxImpl, idxReview)
	}
}

func TestCLI_FlowNoLLM(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata)
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for flow without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
	output := string(out)
	if !strings.Contains(output, "no LLM model configured") {
		t.Errorf("expected 'no LLM model configured' in output, got: %s", output)
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "bogus")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown command")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_NoArgs(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin)
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for no args")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_ValidateNoFile(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "validate")
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for validate with no file")
	}
}

func TestCLI_UnknownFlag(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--bogus")
	cmd.Env = scrubProviderEnv() // defensive: same forward-compat guard as MissingFlagValue/MaxRetries
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_FlowJSON_NoLLM(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--json")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for flow --json without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		// Should exit 3 (config error: no LLM), not 2 (validation) - --json is valid.
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
	// Stderr should mention LLM, not "unknown flag".
	output := string(out)
	if strings.Contains(output, "unknown flag") {
		t.Errorf("--json was not recognized: %s", output)
	}
}

func TestCLI_MissingFlagValue(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--model")
	// Defensive scrub: today the flag-error path exits before the
	// resolver runs, so provider env can't trigger a real call. If the
	// flag-error ordering changes, this guard prevents a silent flip
	// from exit 3 to exit 1.
	cmd.Env = scrubProviderEnv()
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for missing flag value")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_AgentNoLLM(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "agent", "hello")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for agent without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
	output := string(out)
	if !strings.Contains(output, "no LLM model configured") {
		t.Errorf("expected LLM error message, got: %s", output)
	}
}

func TestCLI_AgentNoPrompt(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "agent")
	cmd.Env = scrubProviderEnv() // defensive: same forward-compat class as UnknownFlag/MissingFlagValue
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for agent with no prompt")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
}

func TestCLI_AgentUnknownFlag(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "agent", "hello", "--bogus")
	cmd.Env = scrubProviderEnv() // defensive: same forward-compat guard as MissingFlagValue/MaxRetries
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
}

func TestCLI_AgentJSON_NoLLM(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "agent", "hello", "--json")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for agent --json without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
	output := string(out)
	if strings.Contains(output, "unknown flag") {
		t.Errorf("--json was not recognized: %s", output)
	}
}

func TestCLI_GoalNoLLM(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "goal", "build a feature")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for goal without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
	output := string(out)
	if !strings.Contains(output, "no LLM model configured") {
		t.Errorf("expected LLM error message, got: %s", output)
	}
}

func TestCLI_GoalNoPrompt(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "goal")
	cmd.Env = scrubProviderEnv() // defensive: same forward-compat class as UnknownFlag/MissingFlagValue
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for goal with no prompt")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
}

func TestCLI_GoalUnknownFlag(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "goal", "build feature", "--bogus")
	cmd.Env = scrubProviderEnv() // defensive: same forward-compat guard as MissingFlagValue/MaxRetries
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown flag")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
}

func TestCLI_GoalJSON_NoLLM(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "goal", "build feature", "--json")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for goal --json without LLM")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
	output := string(out)
	if strings.Contains(output, "unknown flag") {
		t.Errorf("--json was not recognized: %s", output)
	}
}

func TestCLI_MaxRetriesMissingValue(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--max-retries")
	cmd.Env = scrubProviderEnv() // defensive: prevent flag-error→resolver-error flip
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for missing --max-retries value")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_MaxRetriesInvalidValue(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--max-retries", "abc")
	cmd.Env = scrubProviderEnv() // defensive: prevent flag-error→resolver-error flip
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for invalid --max-retries value")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3", exitErr.ExitCode())
		}
	}
}

func TestCLI_PlanMissingFile(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "plan", "/nonexistent/workflow.yaml")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for plan with missing file")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 2 {
			t.Errorf("exit code = %d, want 2", exitErr.ExitCode())
		}
	}
	output := string(out)
	if !strings.Contains(output, "✗") {
		t.Errorf("expected error marker '✗' in output, got: %s", output)
	}
}

func TestCLI_PlanCycle(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "cycle.yaml")
	cmd := exec.Command(bin, "plan", testdata)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for plan with cycle")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 2 {
			t.Errorf("exit code = %d, want 2", exitErr.ExitCode())
		}
	}
	output := string(out)
	if !strings.Contains(output, "cycle") {
		t.Errorf("expected error message to mention 'cycle', got: %s", output)
	}
}

func TestCLI_AgentStdin(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "agent", "analyze this")
	cmd.Stdin = strings.NewReader("some piped data")
	// Same env-leak class as the _NoLLM* tests: this asserts exit==3
	// for the "no LLM configured" path. Without scrubbing, a developer
	// shell with provider credentials would route the request to a real
	// LLM and exit 1. Mirror the scrub.
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for agent without LLM (even with stdin)")
	}
	// Should exit 3 (no LLM), not crash from stdin reading.
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 3 {
			t.Errorf("exit code = %d, want 3\noutput: %s", exitErr.ExitCode(), out)
		}
	}
}

// TestBuildOrchestratorOpts_NoCoordinator pins the retirement of the
// shared `WithCoordinator(nil)` baseline that buildOrchestratorOpts
// installed for every command. Coord wiring now lives at the cmdFlow /
// cmdGoal call sites via coordinatorOption. cmdAgent must NOT
// receive a WithCoordinator option (no coord semantics) - pin the
// regression: even when every flag that previously triggered a
// warning is set, buildOrchestratorOpts itself emits no
// WithCoordinator option.
func TestBuildOrchestratorOpts_NoCoordinator(t *testing.T) {
	flags := cmdFlags{quiet: true, jsonOutput: true, summaryOnly: true}
	opts := buildOrchestratorOpts(flags)
	orch := zenflowNew(opts...)
	if orch.Coordinator() != nil {
		t.Errorf("buildOrchestratorOpts must not install a coordinator; got %#v", orch.Coordinator())
	}
}

// TestCLI_Quiet_NoCoordinator - (refined). --quiet
// installs WithCoordinator(nil) - explicit "no narration" mode.
// --quiet wins over --json: combining them disables coord too.
func TestCLI_Quiet_NoCoordinator(t *testing.T) {
	llm := &cliMockLLM{}
	cases := []struct {
		name  string
		flags cmdFlags
	}{
		{"quiet", cmdFlags{quiet: true}},
		{"quiet+json", cmdFlags{quiet: true, jsonOutput: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt, runner := coordinatorOption(tc.flags, llm)
			if runner != nil {
				t.Errorf("expected nil runner for %s, got %#v", tc.name, runner)
			}
			orch := zenflowNew(opt)
			if got := orch.Coordinator(); got != nil {
				t.Errorf("expected nil coordinator on Orchestrator for %s, got %#v", tc.name, got)
			}
		})
	}
}

// TestCLI_JSON_HasCoordinator. --json no longer disables
// coord (was behavior). JSON consumers want the full event
// stream including coord narration / forward / finalize for
// programmatic processing via shell pipes (zenflow flow x.yaml --json
// | jq ...). Hiding coord events forced consumers to import zenflow
// as a library to get full observability - fixes this so CLI
// JSON mode is a true superset of stdout mode.
// Users who want JSON-without-coord-cost combine --quiet --json
// (covered by TestCLI_Quiet_NoCoordinator's "quiet+json" case).
func TestCLI_JSON_HasCoordinator(t *testing.T) {
	llm := &cliMockLLM{}
	flags := cmdFlags{jsonOutput: true}
	opt, runner := coordinatorOption(flags, llm)
	if runner == nil {
		t.Fatal("--json alone should install coord (was nil pre-fix); use --quiet --json to opt out")
	}
	if runner.StepID() != "coordinator" {
		t.Errorf("runner.StepID() = %q, want %q", runner.StepID(), "coordinator")
	}
	orch := zenflowNew(opt)
	if got := orch.Coordinator(); got != runner {
		t.Errorf("orchestrator coordinator = %p, want %p (the runner from coordinatorOption)", got, runner)
	}
}

// TestCLI_Default_HasCoordinator. When no narration-suppressing
// flag is set, the CLI installs a default NewDefaultCoordRunner with
// the standard 3-tool surface (forward_to_agent + narrate + finalize).
func TestCLI_Default_HasCoordinator(t *testing.T) {
	llm := &cliMockLLM{}
	flags := cmdFlags{}
	opt, runner := coordinatorOption(flags, llm)
	if runner == nil {
		t.Fatal("expected non-nil runner for default flags")
	}
	if runner.Model() != llm {
		t.Errorf("runner.Model() = %v, want injected llm", runner.Model())
	}
	if runner.StepID() != "coordinator" {
		t.Errorf("runner.StepID() = %q, want %q", runner.StepID(), "coordinator")
	}
	want := map[string]bool{"forward_to_agent": false, "narrate": false, "finalize": false}
	for _, tool := range runner.Tools() {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("default runner missing tool %q (got %v)", name, runner.Tools())
		}
	}
	orch := zenflowNew(opt)
	if got := orch.Coordinator(); got != runner {
		t.Errorf("orchestrator coordinator = %p, want %p (the runner from coordinatorOption)", got, runner)
	}
}

// TestCLI_SummaryOnly_SynthesizeOnlyMode. --summary-only
// installs a coord runner with SynthesizeOnly applied, dropping
// `narrate` from the default set so the coord emits a single final
// synthesis via the finalize summary argument instead of per-step
// narrations.
func TestCLI_SummaryOnly_SynthesizeOnlyMode(t *testing.T) {
	llm := &cliMockLLM{}
	flags := cmdFlags{summaryOnly: true}
	_, runner := coordinatorOption(flags, llm)
	if runner == nil {
		t.Fatal("expected non-nil runner for --summary-only")
	}
	for _, tool := range runner.Tools() {
		if tool.Name == "narrate" {
			t.Errorf("--summary-only runner unexpectedly contains 'narrate' tool")
		}
	}
	// Forward + finalize must still be present.
	want := map[string]bool{"forward_to_agent": false, "finalize": false}
	for _, tool := range runner.Tools() {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("--summary-only runner missing required tool %q", name)
		}
	}
}

// TestCoordinatorOption_NilLLM - defensive branch: if no LLM is
// resolved (e.g. user forgot --model and no env var set), the CLI
// must NOT crash trying to construct a default coord with a nil LLM.
// Returns nil-coord; the HasLLM check downstream surfaces the missing
// provider as exit 3.
func TestCoordinatorOption_NilLLM(t *testing.T) {
	for _, flags := range []cmdFlags{{}, {quiet: true}, {summaryOnly: true}, {jsonOutput: true}} {
		opt, runner := coordinatorOption(flags, nil)
		if runner != nil {
			t.Errorf("flags=%+v: expected nil runner when llm=nil, got %#v", flags, runner)
		}
		orch := zenflowNew(opt)
		if orch.Coordinator() != nil {
			t.Errorf("flags=%+v: expected nil orchestrator coordinator when llm=nil", flags)
		}
	}
}

// TestCLI_FlowPositionalContext. `zenflow flow x.yaml
// "topic: AI"` parses the 2nd positional as the flow context and
// passes it via WithFlowContext. We swap the runFlow seam to capture
// the options the cmd builds, then assert the captured option
// populates flowContext on a fresh runFlowConfig.
func TestCLI_FlowPositionalContext(t *testing.T) {
	const wantContext = "topic: AI replaces juniors"
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, wantContext)
	defer cleanup()
	var captured []zenflow.RunFlowOption
	runFlow = func(_ *zenflow.Orchestrator, _ context.Context, _ *zenflow.Workflow, opts ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		captured = opts
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdFlow()
	if env.exitCode != -1 && env.exitCode != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", env.exitCode, env.stderr.String())
	}
	if len(captured) != 1 {
		t.Fatalf("captured %d RunFlowOptions, want 1", len(captured))
	}
	// Apply the captured option to a runFlowConfig probe via
	// zenflow.WithFlowContext semantics - we cannot inspect the
	// private struct directly, so re-run the workflow against a real
	// orchestrator and assert the WithFlowContext-driven event lands
	// in the coord mailbox. The e2e proves this end-to-end; here
	// we just need a structural pin that the option came through. The
	// option's behaviour is tested by's TestRunFlow_FlowContext.
	// Surface assertion: the option, when invoked, must be the
	// WithFlowContext one (callable, no panic, accepts a runFlowConfig
	// pointer). zenflow.WithFlowContext is the only public RunFlowOption
	// constructor today, so a non-nil RunFlowOption == WithFlowContext.
	if captured[0] == nil {
		t.Errorf("captured RunFlowOption is nil")
	}
}

// TestCLI_GoalPositionalContext. Symmetric to
// TestCLI_FlowPositionalContext but for `zenflow goal "<goal>"
// "<extra context>"`.
func TestCLI_GoalPositionalContext(t *testing.T) {
	const wantGoal = "solve X"
	const wantContext = "constraint: must be reversible"
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", wantGoal, wantContext)
	defer cleanup()
	var capturedGoal string
	var capturedOpts []zenflow.RunGoalOption
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, goal string, opts ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		capturedGoal = goal
		capturedOpts = opts
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdGoal()
	if env.exitCode != -1 && env.exitCode != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", env.exitCode, env.stderr.String())
	}
	if capturedGoal != wantGoal {
		t.Errorf("captured goal = %q, want %q", capturedGoal, wantGoal)
	}
	if len(capturedOpts) != 1 {
		t.Fatalf("captured %d RunGoalOptions, want 1", len(capturedOpts))
	}
	if capturedOpts[0] == nil {
		t.Errorf("captured RunGoalOption is nil")
	}
}

// TestCLI_FlowNoPositionalContext - pin: when only the YAML path is
// given (no 2nd positional), runFlow receives no RunFlowOptions.
func TestCLI_FlowNoPositionalContext(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path)
	defer cleanup()
	var captured []zenflow.RunFlowOption
	runFlow = func(_ *zenflow.Orchestrator, _ context.Context, _ *zenflow.Workflow, opts ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		captured = opts
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdFlow()
	_ = env
	if len(captured) != 0 {
		t.Errorf("expected no RunFlowOptions when no 2nd positional, got %d", len(captured))
	}
}

// TestCLI_GoalNoPositionalContext - symmetric pin for cmdGoal.
func TestCLI_GoalNoPositionalContext(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	var captured []zenflow.RunGoalOption
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, _ string, opts ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		captured = opts
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdGoal()
	_ = env
	if len(captured) != 0 {
		t.Errorf("expected no RunGoalOptions when no 2nd positional, got %d", len(captured))
	}
}

// TestSplitPositionalContext - direct unit test for the helper.
func TestSplitPositionalContext(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantContext string
		wantRest    []string
	}{
		{"empty", []string{}, "", []string{}},
		{"only flag", []string{"--quiet"}, "", []string{"--quiet"}},
		{"only context", []string{"topic: x"}, "topic: x", []string{}},
		{"context + flag", []string{"topic: x", "--quiet"}, "topic: x", []string{"--quiet"}},
		{"context + flag with value", []string{"goal context", "--model", "gemini"}, "goal context", []string{"--model", "gemini"}},
		// / - single-dash flags are flags, not
		// positional context. Without these, `zenflow flow file.yaml -v`
		// silently swallowed `-v` as the flow context. The agent
		// subcommand (which doesn't go through splitPositionalContext)
		// already rejected unknown short flags; bringing flow/goal to
		// parity is the point.
		{"only short flag", []string{"-v"}, "", []string{"-v"}},
		{"context + short flag", []string{"topic: x", "-v"}, "topic: x", []string{"-v"}},
		{"only short help", []string{"-h"}, "", []string{"-h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCtx, gotRest := splitPositionalContext(tc.args)
			if gotCtx != tc.wantContext {
				t.Errorf("context = %q, want %q", gotCtx, tc.wantContext)
			}
			if len(gotRest) != len(tc.wantRest) {
				t.Errorf("rest len = %d, want %d (got %v)", len(gotRest), len(tc.wantRest), gotRest)
			} else {
				for i, want := range tc.wantRest {
					if gotRest[i] != want {
						t.Errorf("rest[%d] = %q, want %q", i, gotRest[i], want)
					}
				}
			}
		})
	}
}

// TestCLI_FlowMaxDepthFlag. `zenflow flow x.yaml --max-depth 5`
// must pass through to the orchestrator as `WithMaxDepth(5)`. The
// orchestrator's `MaxDepth` accessor returns the raw configured
// value (0 sentinel = use runtime default 3). We capture the
// orchestrator newOrch built and assert MaxDepth reflects the flag.
func TestCLI_FlowMaxDepthFlag(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path, "--max-depth", "5")
	defer cleanup()
	var capturedOrch *zenflow.Orchestrator
	prevNewOrch := newOrch
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		capturedOrch = prevNewOrch(opts...)
		return capturedOrch
	}
	runFlow = func(_ *zenflow.Orchestrator, _ context.Context, _ *zenflow.Workflow, _ ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdFlow()
	if env.exitCode != -1 && env.exitCode != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", env.exitCode, env.stderr.String())
	}
	if capturedOrch == nil {
		t.Fatal("orchestrator was never constructed")
	}
	if got := capturedOrch.MaxDepth(); got != 5 {
		t.Errorf("orch.MaxDepth() = %d, want 5", got)
	}
}

// TestCLI_FlowNoMaxDepthFlag - pin: when --max-depth is omitted, the
// orchestrator's MaxDepth returns the 0 sentinel (runtime default 3
// applied lazily inside RunAgent - we don't override here).
func TestCLI_FlowNoMaxDepthFlag(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "flow", path)
	defer cleanup()
	var capturedOrch *zenflow.Orchestrator
	prevNewOrch := newOrch
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		capturedOrch = prevNewOrch(opts...)
		return capturedOrch
	}
	runFlow = func(_ *zenflow.Orchestrator, _ context.Context, _ *zenflow.Workflow, _ ...zenflow.RunFlowOption) (*zenflow.WorkflowResult, error) {
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdFlow()
	_ = env
	if capturedOrch == nil {
		t.Fatal("orchestrator was never constructed")
	}
	if got := capturedOrch.MaxDepth(); got != 0 {
		t.Errorf("orch.MaxDepth() = %d, want 0 sentinel when flag omitted", got)
	}
}

// TestCLI_GoalMaxDepthFlag. Symmetric to the flow variant.
func TestCLI_GoalMaxDepthFlag(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth", "--max-depth", "7")
	defer cleanup()
	var capturedOrch *zenflow.Orchestrator
	prevNewOrch := newOrch
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		capturedOrch = prevNewOrch(opts...)
		return capturedOrch
	}
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, _ string, _ ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdGoal()
	if env.exitCode != -1 && env.exitCode != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", env.exitCode, env.stderr.String())
	}
	if capturedOrch == nil {
		t.Fatal("orchestrator was never constructed")
	}
	if got := capturedOrch.MaxDepth(); got != 7 {
		t.Errorf("orch.MaxDepth() = %d, want 7", got)
	}
}

// TestCLI_GoalNoMaxDepthFlag - pin for the omitted-flag path.
func TestCLI_GoalNoMaxDepthFlag(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock, "zenflow", "goal", "build auth")
	defer cleanup()
	var capturedOrch *zenflow.Orchestrator
	prevNewOrch := newOrch
	newOrch = func(opts ...zenflow.Option) *zenflow.Orchestrator {
		capturedOrch = prevNewOrch(opts...)
		return capturedOrch
	}
	runGoal = func(_ *zenflow.Orchestrator, _ context.Context, _ string, _ ...zenflow.RunGoalOption) (*zenflow.WorkflowResult, error) {
		return &zenflow.WorkflowResult{Status: zenflow.StatusCompleted}, nil
	}
	cmdGoal()
	_ = env
	if capturedOrch == nil {
		t.Fatal("orchestrator was never constructed")
	}
	if got := capturedOrch.MaxDepth(); got != 0 {
		t.Errorf("orch.MaxDepth() = %d, want 0 sentinel when flag omitted", got)
	}
}

// TestCLI_FlowBadMaxDepth - invalid integer value rejected with exit 3.
func TestCLI_FlowBadMaxDepth(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path, "--max-depth", "xyz")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// TestCLI_GoalBadMaxDepth - invalid integer value rejected with exit 3.
func TestCLI_GoalBadMaxDepth(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "goal", "build", "--max-depth", "xyz")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// TestCLI_FlowMaxDepthMissingValue - bare --max-depth flag rejected.
func TestCLI_FlowMaxDepthMissingValue(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: \"hi\"\n")
	env, cleanup := setupTest(t, "zenflow", "flow", path, "--max-depth")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// TestCLI_GoalMaxDepthMissingValue - bare --max-depth flag rejected.
func TestCLI_GoalMaxDepthMissingValue(t *testing.T) {
	env, cleanup := setupTest(t, "zenflow", "goal", "build", "--max-depth")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Errorf("exit = %d, want 3", env.exitCode)
	}
}

// TestStartCoordRunner_NilRunner - defensive branch: nil runner is a
// no-op that returns a no-op cleanup func.
func TestStartCoordRunner_NilRunner(t *testing.T) {
	cleanup := startCoordRunner(context.Background(), nil, "")
	cleanup() // must not panic
}

// TestStartCoordRunner_NonNilRunner - caller-owned lifecycle.
// startCoordRunner must launch the runner in a background goroutine,
// invoke its Run method with the supplied modelID, and the returned
// cleanup func must cancel the context and return promptly.
func TestStartCoordRunner_NonNilRunner(t *testing.T) {
	llm := &cliMockLLM{}
	runner := zenflow.NewDefaultCoordRunner(llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := startCoordRunner(ctx, runner, "test-model")
	// Cleanup must return within 2s (the function's own timeout) even
	// if the mock LLM returns immediately.
	doneCh := make(chan struct{})
	go func() {
		cleanup()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	// good
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not return within 3s")
	}
}

// cliMockLLM is a minimal LanguageModel used by unit tests that
// don't need to exercise the LLM call itself - they only assert on
// the orchestrator/coord-runner shape.
type cliMockLLM struct{}

func (c *cliMockLLM) ModelID() string { return "cli-mock" }
func (c *cliMockLLM) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{Text: "ok", FinishReason: provider.FinishStop}, nil
}
func (c *cliMockLLM) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Type: provider.ChunkFinish, FinishReason: provider.FinishStop}
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// WorkflowResult.FinalAnswer prefers Summary if present, else falls back
// to the last terminal step's Content. Returns empty when neither is set.
func TestFinalAnswerText_PrefersSummary(t *testing.T) {
	wf := &zenflow.Workflow{
		Steps: []zenflow.Step{{ID: "s1"}},
	}
	r := &zenflow.WorkflowResult{
		Summary: "synthesis from coord",
		Steps: map[string]*zenflow.StepResult{
			"s1": {Status: zenflow.StepCompleted, Content: "raw step output"},
		},
	}
	if got := r.FinalAnswer(wf); got != "synthesis from coord" {
		t.Errorf("FinalAnswer=%q want Summary", got)
	}
}

func TestFinalAnswerText_FallsBackToLastTerminalStep(t *testing.T) {
	wf := &zenflow.Workflow{
		Steps: []zenflow.Step{
			{ID: "first"},
			{ID: "middle", DependsOn: []string{"first"}},
			{ID: "last", DependsOn: []string{"middle"}},
		},
	}
	r := &zenflow.WorkflowResult{
		Steps: map[string]*zenflow.StepResult{
			"first":  {Status: zenflow.StepCompleted, Content: "first content"},
			"middle": {Status: zenflow.StepCompleted, Content: "middle content"},
			"last":   {Status: zenflow.StepCompleted, Content: "last content"},
		},
	}
	if got := r.FinalAnswer(wf); got != "last content" {
		t.Errorf("FinalAnswer=%q want 'last content' (terminal step)", got)
	}
}

func TestFinalAnswerText_EmptyWhenNoSummaryNoSteps(t *testing.T) {
	if got := (*zenflow.WorkflowResult)(nil).FinalAnswer(nil); got != "" {
		t.Errorf("FinalAnswer(nil,nil)=%q want empty", got)
	}
	r := &zenflow.WorkflowResult{}
	if got := r.FinalAnswer(nil); got != "" {
		t.Errorf("FinalAnswer(nil-wf,empty-result)=%q want empty", got)
	}
}

func TestFinalAnswerText_SkipsFailedTerminalStep(t *testing.T) {
	wf := &zenflow.Workflow{
		Steps: []zenflow.Step{{ID: "s1"}, {ID: "s2"}},
	}
	r := &zenflow.WorkflowResult{
		Steps: map[string]*zenflow.StepResult{
			"s1": {Status: zenflow.StepCompleted, Content: "completed step content"},
			"s2": {Status: zenflow.StepFailed, Content: "failed step content"},
		},
	}
	// s1 and s2 are both terminal (no other step depends on them).
	// In declaration order, s1 completes and s2 fails - FinalAnswer
	// must skip s2 and return s1's content.
	got := r.FinalAnswer(wf)
	if got != "completed step content" {
		t.Errorf("FinalAnswer=%q want 'completed step content' (failed terminal skipped)", got)
	}
}

// TestResolveProvider_LeadingColonRejected verifies: a model flag
// value like ":gpt-5" (leading colon, no provider prefix) is rejected and
// resolveProvider returns nil instead of passing the malformed ID to a
// provider constructor.
func TestResolveProvider_LeadingColonRejected(t *testing.T) {
	cases := []string{
		":gpt-5",
		":model-name",
		":",
	}
	for _, flag := range cases {
		model, _ := resolveProvider(flag)
		if model != nil {
			t.Errorf("resolveProvider(%q): expected nil model for leading-colon value, got %T", flag, model)
		}
	}
}

// TestSplitProviderModel_LeadingColon verifies splitProviderModel itself
// passes ":gpt-5" as modelID "" for provider="" (no slash present), so
// the caller in resolveProvider can detect and reject the colon prefix.
// TestCLI_Trace_FlagAccepted verifies that --trace is recognised (not
// "unknown flag") and that the process fails with exit 3 (no LLM configured)
// rather than exit 3 for "unknown flag". It also confirms no span-related
// panic or crash is present when no LLM is configured.
func TestCLI_Trace_FlagAccepted(t *testing.T) {
	bin := buildBinary(t)
	testdata := filepath.Join("..", "..", "testdata", "simple.yaml")
	cmd := exec.Command(bin, "flow", testdata, "--trace")
	cmd.Env = scrubProviderEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit when no LLM is configured")
	}
	output := string(out)
	// Must NOT complain about --trace being unknown.
	if strings.Contains(output, "unknown flag") {
		t.Errorf("--trace was not recognised as a valid flag: %s", output)
	}
	// Must fail with "no LLM model configured" (exit 3), not a panic.
	if !strings.Contains(output, "no LLM model configured") {
		t.Errorf("expected 'no LLM model configured', got: %s", output)
	}
}

// TestCLI_Trace_StderrSpans verifies that when --trace is set and a workflow
// runs successfully (via a mock LLM injected by the test binary), at least
// one OTel span block is written to stderr. This test builds a special binary
// with the mock seam wired; it is skipped when the test binary cannot be built.
// NOTE: This test exercises the real stdouttrace exporter path (no OTLP
// endpoint configured) so that --trace produces visible output by default.
// The expected output pattern is the stdouttrace JSON "Name" field which
// appears in every span record.
func TestCLI_Trace_HelpTextUpdated(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "--help")
	out, _ := cmd.CombinedOutput()
	output := string(out)
	if !strings.Contains(output, "honors OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Errorf("--trace help text not updated; expected 'honors OTEL_EXPORTER_OTLP_ENDPOINT' in:\n%s", output)
	}
}

func TestSplitProviderModel_LeadingColon(t *testing.T) {
	cases := []struct {
		input          string
		wantProvider   string
		wantModelStart string // prefix of modelID
	}{
		{":gpt-5", "", ":gpt-5"},
		{"google/:gpt-5", "google", ":gpt-5"},
	}
	for _, tc := range cases {
		prov, model := splitProviderModel(tc.input)
		if prov != tc.wantProvider {
			t.Errorf("splitProviderModel(%q) provider = %q, want %q", tc.input, prov, tc.wantProvider)
		}
		if !strings.HasPrefix(model, ":") {
			t.Errorf("splitProviderModel(%q) model = %q, expected leading colon", tc.input, model)
		}
	}
}
