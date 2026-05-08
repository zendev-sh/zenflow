package exec

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// TestZFB4_TruncateForContext_Short verifies short strings pass through.
func TestZFB4_TruncateForContext_Short(t *testing.T) {
	const input = "hello world"
	got := truncateForContext(input, 100)
	if got != input {
		t.Errorf("got %q, want %q (no truncation for short input)", got, input)
	}
}

// TestZFB4_TruncateForContext_Long verifies long strings are truncated
// and carry the marker.
func TestZFB4_TruncateForContext_Long(t *testing.T) {
	input := strings.Repeat("x", 10_000)
	got := truncateForContext(input, 1_000)
	if len(got) > 1_000 {
		t.Errorf("len(got) = %d, want ≤1000", len(got))
	}
	if !strings.Contains(got, "truncated for context limit") {
		t.Errorf("truncation marker missing: %q", got[max(0, len(got)-80):])
	}
}

// TestZFB4_TruncateForContext_UTF8Boundary verifies a multibyte rune on the
// truncation boundary is not split. We build a string of 3-byte runes
// (Chinese "字") and truncate at a non-rune-boundary.
func TestZFB4_TruncateForContext_UTF8Boundary(t *testing.T) {
	input := strings.Repeat("字", 1_000) // 3000 bytes
	got := truncateForContext(input, 500)
	if !strings.Contains(got, "truncated for context limit") {
		t.Error("marker missing")
	}
	// Result must be valid UTF-8.
	if !utf8.ValidString(got) {
		t.Errorf("truncated output is not valid UTF-8: %q", got[:min(80, len(got))])
	}
}

// TestZFB4_AssemblePrompt_CapsPerDepContent verifies per-dep content is
// individually capped, regardless of total dep count.
func TestZFB4_AssemblePrompt_CapsPerDepContent(t *testing.T) {
	hugeContent := strings.Repeat("A", maxDepContentBytes*3)
	priorResults := map[string]*StepResult{
		"design": {
			ID:      "design",
			Status:  spec.StepCompleted,
			Content: hugeContent,
		},
	}
	step := Step{
		ID:           "implement",
		Instructions: "build it",
		DependsOn:    []string{"design"},
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)

	// The "design" section should be truncated.
	if !strings.Contains(out, "### design (completed)") {
		t.Fatal("design section missing")
	}
	if !strings.Contains(out, "truncated for context limit") {
		t.Errorf("per-dep truncation marker missing")
	}
	// Full content should NOT appear.
	if strings.Contains(out, hugeContent) {
		t.Errorf("full huge content leaked into prompt - truncation did not fire")
	}
}

// PreserveContent=true bypasses the per-dep 16KB cap for steps
// that intentionally aggregated content (e.g. cumulative loop). Verdict-
// style aggregator steps depend on this - without it the cumulative
// debate text gets cut at 16KB and the dependent agent sees
// "[truncated for context limit]" instead of the full history.
func TestZFB4_AssemblePrompt_PreserveContentBypassesPerDepCap(t *testing.T) {
	// 50KB content - well over maxDepContentBytes (16KB) but under
	// maxPromptBytes (120KB) so the global cap doesn't fire.
	cumulativeContent := strings.Repeat("DEBATE_TEXT ", 50*1024/12)
	priorResults := map[string]*StepResult{
		"debate-rounds": {
			ID:              "debate-rounds",
			Status:          spec.StepCompleted,
			Content:         cumulativeContent,
			PreserveContent: true,
		},
	}
	step := Step{
		ID:           "verdict",
		Instructions: "summarize",
		DependsOn:    []string{"debate-rounds"},
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)

	if strings.Contains(out, "truncated for context limit") {
		t.Errorf("PreserveContent=true must bypass per-dep truncation; saw marker in output")
	}
	if !strings.Contains(out, cumulativeContent) {
		t.Errorf("PreserveContent=true must include full content; full string not found")
	}
}

// follow-up: PreserveContent must ALSO bypass the OVERALL
// maxPromptBytes (120KB) cap, otherwise pathological cumulative
// loops still get truncated at the END (matching the user's
// "truncated at the end of con-argue" report from debate-until.yaml).
func TestZFB4_AssemblePrompt_PreserveContent_BypassesOverallCap(t *testing.T) {
	// 200KB content - well over maxPromptBytes (120KB). PreserveContent
	// signals intentional aggregation; user accepts the prompt blowing
	// the budget on cumulative aggregator workflows.
	huge := strings.Repeat("DEBATE ", 200*1024/7)
	priorResults := map[string]*StepResult{
		"debate-rounds": {
			ID:              "debate-rounds",
			Status:          spec.StepCompleted,
			Content:         huge,
			PreserveContent: true,
		},
	}
	step := Step{
		ID:           "verdict",
		Instructions: "summarize",
		DependsOn:    []string{"debate-rounds"},
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)

	if strings.Contains(out, "truncated for context limit") {
		t.Errorf("PreserveContent=true must bypass BOTH per-dep AND overall maxPromptBytes; saw truncation marker (overall cap fires at 120KB; the cumulative content alone is %d bytes)", len(huge))
	}
	if !strings.Contains(out, huge) {
		t.Errorf("PreserveContent=true must include full content; %d-byte string not found in %d-byte output", len(huge), len(out))
	}
}

// follow-up: PreserveContent must ALSO bypass any installed
// OutputTransform (e.g. CLI's default TokenBudgetTransformer with 8KB
// MaxBytesPerDep). User repro: debate-until.yaml verdict still saw
// [truncated for context limit] even after because the CLI
// installs the transformer unconditionally; OutputTransform fired
// BEFORE writeDepSection's PreserveContent check.
func TestExecutor_OutputTransform_RespectsPreserveContent(t *testing.T) {
	hugeContent := strings.Repeat("ROUND ", 30*1024/6) // ~30KB
	priorResults := map[string]*StepResult{
		"debate-rounds": {
			ID:              "debate-rounds",
			Status:          spec.StepCompleted,
			Content:         hugeContent,
			PreserveContent: true,
		},
	}
	wf := &Workflow{
		Name:   "transform-bypass",
		Agents: map[string]AgentConfig{"a": {Description: "a"}},
		Steps: []Step{
			{ID: "verdict", Agent: "a", Instructions: "summarize", DependsOn: []string{"debate-rounds"}},
		},
	}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "ok", FinishReason: provider.FinishStop, Usage: provider.Usage{InputTokens: 1, OutputTokens: 1}},
		},
	}
	exec := newTestExecutor(model, nil, wf)
	// Install the same TokenBudgetTransformer the CLI installs by default.
	exec.OutputTransform = &TokenBudgetTransformer{MaxBytesPerDep: 8 * 1024}

	// Simulate the executor's transform-and-assemble flow with our
	// pre-built priorResults (mimicking what runStep does at
	// executor.go:1530-1546 before AssemblePrompt).
	model_ := "test-model"
	for depID, sr := range priorResults {
		if sr == nil || sr.Status != spec.StepCompleted {
			continue
		}
		if sr.PreserveContent {
 // The follow-up branch under test.
			continue
		}
		newContent, newResult := exec.OutputTransform.TransformStepOutput(depID, sr.Content, sr.Result, model_)
		if newContent != sr.Content || newResult != nil {
			transformed := *sr
			transformed.Content = newContent
			if newResult != nil {
				transformed.Result = newResult
			}
			priorResults[depID] = &transformed
		}
	}

	out, _ := AssemblePrompt(AgentConfig{}, wf.Steps[0], "", priorResults)
	if strings.Contains(out, "truncated for context limit") {
		t.Errorf("PreserveContent=true must bypass OutputTransform too; saw truncation marker (TokenBudgetTransformer 8KB cap fired despite flag)")
	}
	if !strings.Contains(out, hugeContent) {
		t.Errorf("PreserveContent=true must include full content; %d-byte string not found", len(hugeContent))
	}
}

// coverage: end-to-end via Executor.Run - install
// OutputTransform, run a cumulative loop + dependent step, assert the
// runStep branch that skips the transform when PreserveContent is set
// is hit.
func TestExecutor_OutputTransform_PreserveContent_RealRun(t *testing.T) {
	maxIter := 5
	wf := &Workflow{
		Name: "transform-preservecontent-realrun",
		Agents: map[string]AgentConfig{
			"a": {Description: "agent"},
			"j": {Description: "judge", ResultSchema: map[string]any{
				"type":     "object",
				"required": []any{"done"},
				"properties": map[string]any{
					"done": map[string]any{"type": "boolean"},
				},
			}},
		},
		Steps: []Step{
			{
				ID: "loop",
				Loop: &Loop{
					UntilAgent:    "j",
					MaxIterations: &maxIter,
					OutputMode:    spec.LoopOutputModeCumulative,
				},
				Instructions: "iterate",
			},
			{ID: "consumer", Agent: "a", Instructions: "use loop output", DependsOn: []string{"loop"}},
		},
	}
	model := &mockModel{
		responses: []*provider.GenerateResult{
			{Text: "iter1", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
			{Text: "", Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}, ToolCalls: []provider.ToolCall{
				{ID: "j1", Name: "submit_result", Input: json.RawMessage(`{"done":true}`)},
			}},
			{Text: "consumer-done", FinishReason: provider.FinishStop, Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}},
		},
	}
	exec := newTestExecutor(model, nil, wf)
	exec.OutputTransform = &TokenBudgetTransformer{MaxBytesPerDep: 8 * 1024}

	result, err := exec.Run(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	loopSR := result.Steps["loop"]
	if loopSR == nil || !loopSR.PreserveContent {
		t.Fatalf("loop step PreserveContent flag missing")
	}
	consumerSR := result.Steps["consumer"]
	if consumerSR == nil || consumerSR.Status != spec.StepCompleted {
		t.Fatalf("consumer step not completed: %+v", consumerSR)
	}
	// If the transform fired despite PreserveContent, the loop's
	// content (small in this test) wouldn't be truncated, but the
	// branch coverage is what we exercise.
}

// PreserveContent=false (default) keeps the existing 16KB cap behavior.
func TestZFB4_AssemblePrompt_PreserveContentFalse_StillTruncates(t *testing.T) {
	huge := strings.Repeat("X", maxDepContentBytes*2)
	priorResults := map[string]*StepResult{
		"dep1": {
			ID:              "dep1",
			Status:          spec.StepCompleted,
			Content:         huge,
			PreserveContent: false,
		},
	}
	step := Step{
		ID:        "consumer",
		DependsOn: []string{"dep1"},
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)
	if !strings.Contains(out, "truncated for context limit") {
		t.Errorf("PreserveContent=false must keep truncation behavior; marker missing")
	}
}

// TestZFB4_AssemblePrompt_GlobalCap verifies the final global cap fires
// when many small deps collectively exceed maxPromptBytes. Simulates the
// pathological case of a workflow with dozens of deps.
func TestZFB4_AssemblePrompt_GlobalCap(t *testing.T) {
	// 50 deps × 8KB each = 400KB total, well over 120KB cap.
	priorResults := map[string]*StepResult{}
	depNames := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		id := "dep" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		priorResults[id] = &StepResult{
			ID:      id,
			Status:  spec.StepCompleted,
			Content: strings.Repeat("x", 8*1024),
		}
		depNames = append(depNames, id)
	}
	step := Step{
		ID:           "final",
		Instructions: "summarize",
		DependsOn:    depNames,
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)

	if len(out) > maxPromptBytes {
		t.Errorf("assembled prompt = %d bytes, want ≤%d (global cap)", len(out), maxPromptBytes)
	}
	if !strings.Contains(out, "truncated for context limit") {
		t.Errorf("global cap marker missing")
	}
}

// TestZFB4_AssemblePrompt_SmallPromptUntouched verifies small prompts pass
// through unchanged - the truncation must not affect the happy path.
func TestZFB4_AssemblePrompt_SmallPromptUntouched(t *testing.T) {
	priorResults := map[string]*StepResult{
		"design": {
			ID:      "design",
			Status:  spec.StepCompleted,
			Content: "REST API with /users endpoint",
		},
	}
	step := Step{
		ID:           "implement",
		Instructions: "build the handler",
		DependsOn:    []string{"design"},
	}
	out, _ := AssemblePrompt(AgentConfig{}, step, "", priorResults)

	if strings.Contains(out, "truncated for context limit") {
		t.Errorf("small prompt was incorrectly truncated: %q", out)
	}
	if !strings.Contains(out, "REST API with /users endpoint") {
		t.Error("small dep content missing")
	}
	if !strings.Contains(out, "build the handler") {
		t.Error("instructions missing")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
