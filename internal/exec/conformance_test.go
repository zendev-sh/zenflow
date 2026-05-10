package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"gopkg.in/yaml.v3"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// TestCase structures for spec/v1/testcases

type testCaseFile struct {
	Description string       `yaml:"description"`
	Input       string       `yaml:"input"`
	Expected    testExpected `yaml:"expected"`
}

type testExpected struct {
	Valid           bool             `yaml:"valid"`
	Error           string           `yaml:"error"`
	StepCount       int              `yaml:"step_count"`
	TopoOrder       []string         `yaml:"topo_order"`
	TopoConstraints []topoConstraint `yaml:"topo_constraints"`
}

type topoConstraint struct {
	Before []string `yaml:"before"`
	After  []string `yaml:"after"`
}

// 1. TestSpecConformance

// errorCategoryToType maps test case error categories to Go error type checkers.
var errorCategoryToType = map[string]func(error) bool{
	"missing_name": func(err error) bool {
		var target *MissingNameError
		return errors.As(err, &target)
	},
	"missing_steps": func(err error) bool {
		var target *NoStepsError
		return errors.As(err, &target)
	},
	"empty_steps": func(err error) bool {
		var target *NoStepsError
		return errors.As(err, &target)
	},
	"duplicate_step": func(err error) bool {
		var target *DuplicateStepError
		return errors.As(err, &target)
	},
	"missing_agent": func(err error) bool {
		var target *MissingAgentError
		return errors.As(err, &target)
	},
	"missing_dep": func(err error) bool {
		var target *MissingDepError
		return errors.As(err, &target)
	},
	"cycle": func(err error) bool {
		var target *CycleError
		return errors.As(err, &target)
	},
	"include_has_agent": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"include_has_instructions": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"include_has_loop": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"include_has_condition": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"include_has_contextfiles": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"include_has_model": func(err error) bool {
		var target *IncludeConflictError
		return errors.As(err, &target)
	},
	"loop_missing_max_iterations": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"foreach_with_max_iterations": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"foreach_with_until": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"foreach_with_untilagent": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"until_and_untilagent_exclusive": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"foreach_with_delay": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"foreach_empty_array": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"nested_loop_prohibited": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"negative_value": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
	"unknown_version": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
	"invalid_step_id": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
	"agent_missing_description": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
	"untilagent_bad_ref": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
	"loop_validation": func(err error) bool {
		var target *LoopValidationError
		return errors.As(err, &target)
	},
	"validation": func(err error) bool {
		var target *ValidationError
		return errors.As(err, &target)
	},
}

func TestSpecConformance(t *testing.T) {
	testcasesDir := filepath.Join("spec", "v1", "testcases")

	t.Run("valid", func(t *testing.T) {
		validDir := filepath.Join(testcasesDir, "valid")
		entries, err := os.ReadDir(validDir)
		if err != nil {
			t.Fatalf("cannot read valid testcases dir: %v", err)
		}
		yamlFiles := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".yaml") {
				yamlFiles++
			}
		}
		if yamlFiles == 0 {
			t.Fatal("no .yaml test case files found in valid dir")
		}

		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			t.Run(name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(validDir, entry.Name()))
				if err != nil {
					t.Fatalf("read test case: %v", err)
				}
				var tc testCaseFile
				if err := yaml.Unmarshal(data, &tc); err != nil {
					t.Fatalf("unmarshal test case: %v", err)
				}

				wf, err := ParseWorkflow([]byte(tc.Input))
				if err != nil {
					t.Fatalf("ParseWorkflow returned error for valid case %q: %v\ndescription: %s",
						entry.Name(), err, tc.Description)
				}

				// Check step_count.
				// step_count 0 means "not specified in test case" - skip check.
				if tc.Expected.StepCount > 0 {
					if len(wf.Steps) != tc.Expected.StepCount {
						t.Errorf("step_count = %d, want %d", len(wf.Steps), tc.Expected.StepCount)
					}
				}

				// Check topo_order (exact)
				if len(tc.Expected.TopoOrder) > 0 {
					order, err := TopoSort(wf.Steps)
					if err != nil {
						t.Fatalf("TopoSort failed: %v", err)
					}
					if len(order) != len(tc.Expected.TopoOrder) {
						t.Fatalf("topo_order length = %d, want %d: got %v",
							len(order), len(tc.Expected.TopoOrder), order)
					}
					for i, want := range tc.Expected.TopoOrder {
						if order[i] != want {
							t.Errorf("topo_order[%d] = %q, want %q (full: %v)", i, order[i], want, order)
						}
					}
				}

				// Check topo_constraints (partial ordering)
				if len(tc.Expected.TopoConstraints) > 0 {
					order, err := TopoSort(wf.Steps)
					if err != nil {
						t.Fatalf("TopoSort failed: %v", err)
					}
					indexOf := make(map[string]int, len(order))
					for i, id := range order {
						indexOf[id] = i
					}
					for _, c := range tc.Expected.TopoConstraints {
						for _, before := range c.Before {
							for _, after := range c.After {
								bi, bok := indexOf[before]
								ai, aok := indexOf[after]
								if !bok {
									t.Errorf("topo_constraint: %q not in order %v", before, order)
									continue
								}
								if !aok {
									t.Errorf("topo_constraint: %q not in order %v", after, order)
									continue
								}
								if bi >= ai {
									t.Errorf("topo_constraint violated: %q (pos %d) should be before %q (pos %d) in %v",
										before, bi, after, ai, order)
								}
							}
						}
					}
				}

				// Verify parsed fields for feature-specific test cases.
				switch name {
				case "loop-repeat-until":
					// step[1] = review-cycle has loop with maxIterations
					if wf.Steps[1].Loop == nil {
						t.Error("step loop is nil")
					} else if wf.Steps[1].Loop.MaxIterations == nil {
						t.Error("Loop.MaxIterations is nil")
					}
				case "loop-untilagent":
					// step[0] = debate has loop with untilAgent
					if wf.Steps[0].Loop == nil {
						t.Error("step loop is nil")
					} else if wf.Steps[0].Loop.UntilAgent == "" {
						t.Error("Loop.UntilAgent is empty")
					}
				case "loop-foreach":
					// step[1] = deploy-each has loop with forEach (CEL expression string)
					if wf.Steps[1].Loop == nil {
						t.Error("step loop is nil")
					} else if wf.Steps[1].Loop.ForEach == nil {
						t.Error("Loop.ForEach is nil")
					}
				case "loop-foreach-static":
					// step[0] = review-repos has loop with forEach (static array)
					if wf.Steps[0].Loop == nil {
						t.Error("step loop is nil")
					} else if wf.Steps[0].Loop.ForEach == nil {
						t.Error("Loop.ForEach is nil")
					}
				case "condition":
					// step[1] = deploy has condition
					if wf.Steps[1].Condition == nil {
						t.Error("Step.Condition is empty")
					}
				case "condition-with-loop":
					// step[1] = fix-loop has both condition and loop
					if wf.Steps[1].Condition == nil {
						t.Error("Step.Condition is empty")
					}
					if wf.Steps[1].Loop == nil {
						t.Error("Step.Loop is nil")
					}
				case "include-named":
					// step[0]=setup-auth has include, workflow has includes map
					if wf.Steps[0].Include == "" {
						t.Error("Step.Include is empty")
					}
					if len(wf.Includes) == 0 {
						t.Error("Workflow.Includes is empty")
					}
				case "include-inline-path":
					// step[1] = run-auth has include (inline path)
					if wf.Steps[1].Include == "" {
						t.Error("Step.Include is empty")
					}
				case "full-featured":
					// Check multiple features
					if len(wf.Includes) == 0 {
						t.Error("Workflow.Includes is empty")
					}
					if wf.Options.Scheduler == "" {
						t.Error("Options.Scheduler is empty")
					}
					// Check agent ResultSchema
					if judge, ok := wf.Agents["judge"]; ok {
						if judge.ResultSchema == nil {
							t.Error("judge.ResultSchema is nil")
						}
					} else {
						t.Error("judge agent not found")
					}
				case "all-options":
					if wf.Options.Scheduler == "" {
						t.Error("Options.Scheduler is empty")
					}
				}
			})
		}
	})

	t.Run("invalid", func(t *testing.T) {
		invalidDir := filepath.Join(testcasesDir, "invalid")
		entries, err := os.ReadDir(invalidDir)
		if err != nil {
			t.Fatalf("cannot read invalid testcases dir: %v", err)
		}
		yamlFiles := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".yaml") {
				yamlFiles++
			}
		}
		if yamlFiles == 0 {
			t.Fatal("no .yaml test case files found in invalid dir")
		}

		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			t.Run(name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(invalidDir, entry.Name()))
				if err != nil {
					t.Fatalf("read test case: %v", err)
				}
				var tc testCaseFile
				if err := yaml.Unmarshal(data, &tc); err != nil {
					t.Fatalf("unmarshal test case: %v", err)
				}

				_, parseErr := ParseWorkflow([]byte(tc.Input))
				if parseErr == nil {
					t.Fatalf("ParseWorkflow should have returned error for invalid case %q\ndescription: %s",
						entry.Name(), tc.Description)
				}

				// Check error category matches expected Go error type.
				checker, ok := errorCategoryToType[tc.Expected.Error]
				if !ok {
					t.Fatalf("unknown error category %q - add it to errorCategoryToType", tc.Expected.Error)
				}
				if !checker(parseErr) {
					t.Errorf("error category %q: got error type %T (%v), does not match expected Go type",
						tc.Expected.Error, parseErr, parseErr)
				}
			})
		}
	})
}

// 2. TestStructFields - verify new struct fields exist and serialize correctly

func TestStructFields_StepResultContentAndResult(t *testing.T) {
	// StepResult must have Content (string) and Result (map[string]any), NOT Output.
	sr := StepResult{
		ID:      "test",
		Status:  spec.StepCompleted,
		Content: "hello world",
		Result:  map[string]any{"key": "value", "count": 42.0},
	}

	if sr.Content != "hello world" {
		t.Errorf("StepResult.Content = %q, want %q", sr.Content, "hello world")
	}
	if sr.Result == nil {
		t.Fatal("StepResult.Result is nil, want non-nil map")
	}
	if sr.Result["key"] != "value" {
		t.Errorf("StepResult.Result[key] = %v, want %q", sr.Result["key"], "value")
	}
}

func TestStructFields_AgentConfigResultSchema(t *testing.T) {
	cfg := AgentConfig{
		Description: "test agent",
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"done":   map[string]any{"type": "boolean"},
				"reason": map[string]any{"type": "string"},
			},
			"required": []any{"done", "reason"},
		},
	}

	if cfg.ResultSchema == nil {
		t.Fatal("AgentConfig.ResultSchema is nil")
	}
	props, ok := cfg.ResultSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("ResultSchema.properties type = %T, want map[string]any", cfg.ResultSchema["properties"])
	}
	if _, ok := props["done"]; !ok {
		t.Error("ResultSchema missing 'done' property")
	}
}

func TestStructFields_StepConditionIncludeLoop(t *testing.T) {
	step := Step{
		ID:        "test-step",
		Condition: strPtr("steps.prev.status == 'completed'"),
		Include:   "workflows/sub.yaml",
		Loop: &Loop{
			MaxIterations:  intPtr(5),
			Until:          strPtr("content.contains('done')"),
			UntilAgent:     "judge",
			ForEach:        []any{"a", "b", "c"},
			MaxConcurrency: 3,
			Delay:          Duration(0), // zero value
			Steps: []Step{
				{ID: "inner", Instructions: "do inner work"},
			},
		},
	}

	if step.Condition == nil || *step.Condition != "steps.prev.status == 'completed'" {
		t.Errorf("Step.Condition = %v", step.Condition)
	}
	if step.Include != "workflows/sub.yaml" {
		t.Errorf("Step.Include = %q", step.Include)
	}
	if step.Loop == nil {
		t.Fatal("Step.Loop is nil")
	}
	if step.Loop.MaxIterations == nil || *step.Loop.MaxIterations != 5 {
		t.Errorf("Loop.MaxIterations = %v, want 5", step.Loop.MaxIterations)
	}
	if step.Loop.Until == nil || *step.Loop.Until != "content.contains('done')" {
		t.Errorf("Loop.Until = %v", step.Loop.Until)
	}
	if step.Loop.UntilAgent != "judge" {
		t.Errorf("Loop.UntilAgent = %q", step.Loop.UntilAgent)
	}
	if step.Loop.MaxConcurrency != 3 {
		t.Errorf("Loop.MaxConcurrency = %d, want 3", step.Loop.MaxConcurrency)
	}
	if len(step.Loop.Steps) != 1 {
		t.Errorf("Loop.Steps count = %d, want 1", len(step.Loop.Steps))
	}
}

func TestStructFields_WorkflowOptionsScheduler(t *testing.T) {
	opts := WorkflowOptions{
		Scheduler: "round-robin",
	}
	if opts.Scheduler != "round-robin" {
		t.Errorf("WorkflowOptions.Scheduler = %q, want %q", opts.Scheduler, "round-robin")
	}
}

func TestStructFields_WorkflowIncludes(t *testing.T) {
	wf := Workflow{
		Name: "test",
		Includes: map[string]string{
			"auth":   "workflows/auth.yaml",
			"deploy": "workflows/deploy.yaml",
		},
		Steps: []Step{{ID: "s1"}},
	}
	if len(wf.Includes) != 2 {
		t.Errorf("Workflow.Includes count = %d, want 2", len(wf.Includes))
	}
	if wf.Includes["auth"] != "workflows/auth.yaml" {
		t.Errorf("Includes[auth] = %q", wf.Includes["auth"])
	}
}

func TestStructFields_YAMLRoundTrip(t *testing.T) {
	// Test YAML marshal → unmarshal round-trip for new fields.
	input := `
name: roundtrip-test
version: 1
agents:
  judge:
    description: "Evaluates completion"
    resultSchema:
      type: object
      properties:
        done:
          type: boolean
        reason:
          type: string
      required:
        - done
        - reason
includes:
  deploy: "workflows/deploy.yaml"
steps:
  - id: step1
    agent: judge
    instructions: "Do work"
    condition: "steps.prev.status == 'completed'"
  - id: step2
    include: deploy
    dependsOn: [step1]
    timeout: "15m"
    retries: 1
  - id: step3
    agent: judge
    instructions: "Loop work"
    dependsOn: [step1]
    loop:
      maxIterations: 3
      untilAgent: judge
      delay: "10s"
      steps:
        - id: inner1
          agent: judge
          instructions: "Inner step"
options:
  scheduler: round-robin
  maxConcurrency: 4
`
	wf, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}

	// Verify parsed fields
	if wf.Includes["deploy"] != "workflows/deploy.yaml" {
		t.Errorf("Includes[deploy] = %q", wf.Includes["deploy"])
	}
	if wf.Options.Scheduler != "round-robin" {
		t.Errorf("Options.Scheduler = %q", wf.Options.Scheduler)
	}

	// Check agent resultSchema parsed
	judge := wf.Agents["judge"]
	if judge.ResultSchema == nil {
		t.Fatal("judge.ResultSchema is nil after parse")
	}

	// Step condition
	if wf.Steps[0].Condition == nil || *wf.Steps[0].Condition != "steps.prev.status == 'completed'" {
		t.Errorf("step1.Condition = %v", wf.Steps[0].Condition)
	}

	// Step include
	if wf.Steps[1].Include != "deploy" {
		t.Errorf("step2.Include = %q", wf.Steps[1].Include)
	}

	// Step loop
	if wf.Steps[2].Loop == nil {
		t.Fatal("step3.Loop is nil")
	}
	if wf.Steps[2].Loop.MaxIterations == nil || *wf.Steps[2].Loop.MaxIterations != 3 {
		t.Errorf("step3.Loop.MaxIterations = %v", wf.Steps[2].Loop.MaxIterations)
	}
	if wf.Steps[2].Loop.UntilAgent != "judge" {
		t.Errorf("step3.Loop.UntilAgent = %q", wf.Steps[2].Loop.UntilAgent)
	}
	if len(wf.Steps[2].Loop.Steps) != 1 {
		t.Errorf("step3.Loop.Steps count = %d", len(wf.Steps[2].Loop.Steps))
	}

	// Marshal back to YAML and re-parse to verify round-trip.
	out, err := yaml.Marshal(wf)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	wf2, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("re-ParseWorkflow: %v", err)
	}
	if wf2.Includes["deploy"] != "workflows/deploy.yaml" {
		t.Errorf("round-trip Includes[deploy] = %q", wf2.Includes["deploy"])
	}
	if wf2.Options.Scheduler != "round-robin" {
		t.Errorf("round-trip Scheduler = %q", wf2.Options.Scheduler)
	}
	if wf2.Steps[0].Condition == nil || *wf2.Steps[0].Condition != "steps.prev.status == 'completed'" {
		t.Errorf("round-trip step1.Condition = %v", wf2.Steps[0].Condition)
	}
}

// 3. TestPromptAssemblyContent - uses Content + Result, not Output

func TestPromptAssemblyContent(t *testing.T) {
	agent := AgentConfig{
		Description: "test agent",
		Prompt:      "You are helpful.",
	}
	step := Step{
		ID:           "current",
		Instructions: "Do the thing",
		DependsOn:    []string{"prev"},
	}
	priorResults := map[string]*StepResult{
		"prev": {
			ID:      "prev",
			Status:  spec.StepCompleted,
			Content: "hello from previous step",
			Result:  map[string]any{"key": "value", "count": 42},
		},
	}

	prompt, _ := AssemblePrompt(agent, step, "", priorResults)

	// Must contain the Content text.
	if !strings.Contains(prompt, "hello from previous step") {
		t.Errorf("prompt does not contain Content text.\nprompt:\n%s", prompt)
	}

	// Must contain the JSON-serialized Result.
	if !strings.Contains(prompt, `"key":"value"`) && !strings.Contains(prompt, `"key": "value"`) {
		t.Errorf("prompt does not contain Result JSON.\nprompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"count":42`) && !strings.Contains(prompt, `"count": 42`) {
		t.Errorf("prompt does not contain Result count.\nprompt:\n%s", prompt)
	}
}

func TestPromptAssemblyContent_NilResult(t *testing.T) {
	// When Result is nil, prompt should still include Content.
	agent := AgentConfig{Description: "test"}
	step := Step{
		ID:        "current",
		DependsOn: []string{"prev"},
	}
	priorResults := map[string]*StepResult{
		"prev": {
			ID:      "prev",
			Status:  spec.StepCompleted,
			Content: "just text, no structured result",
			Result:  nil,
		},
	}

	prompt, _ := AssemblePrompt(agent, step, "", priorResults)
	if !strings.Contains(prompt, "just text, no structured result") {
		t.Errorf("prompt missing Content when Result is nil.\nprompt:\n%s", prompt)
	}
	// Should NOT contain "result" section header for nil Result.
	if strings.Contains(prompt, "result\n{") {
		t.Errorf("prompt contains result JSON section when Result is nil.\nprompt:\n%s", prompt)
	}
}

// 4. TestSubmitResultTool - submit_result tool auto-generation and behavior

func TestSubmitResultTool_AutoGenerated(t *testing.T) {
	// Agent with ResultSchema should auto-generate a submit_result ToolDef.
	cfg := AgentConfig{
		Description: "judge agent",
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"done":   map[string]any{"type": "boolean"},
				"reason": map[string]any{"type": "string"},
			},
			"required": []any{"done", "reason"},
		},
	}

	toolDef := SubmitResultToolDef(cfg.ResultSchema)

	if toolDef.Name != "submit_result" {
		t.Errorf("tool name = %q, want %q", toolDef.Name, "submit_result")
	}
	if toolDef.Description == "" {
		t.Error("tool description is empty")
	}

	// Parameters should match the result schema.
	var params map[string]any
	if err := json.Unmarshal(toolDef.InputSchema, &params); err != nil {
		t.Fatalf("unmarshal tool parameters: %v", err)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties type = %T, want map", params["properties"])
	}
	if _, ok := props["done"]; !ok {
		t.Error("submit_result parameters missing 'done'")
	}
	if _, ok := props["reason"]; !ok {
		t.Error("submit_result parameters missing 'reason'")
	}
}

func TestSubmitResultTool_NoSchemaNoTool(t *testing.T) {
	// Agent without ResultSchema should NOT generate submit_result tool.
	cfg := AgentConfig{
		Description: "regular agent",
	}

	toolDef := SubmitResultToolDef(cfg.ResultSchema)
	if toolDef.Name != "" {
		t.Errorf("expected empty ToolDef for nil schema, got name=%q", toolDef.Name)
	}
}

func TestSubmitResultTool_ExtractsResult(t *testing.T) {
	// Calling submit_result should extract the result into AgentResult.Result.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	handler := NewSubmitResultHandler(schema)

	args := json.RawMessage(`{"done": true, "reason": "looks good"}`)
	result, terminated, err := handler.Handle(args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !terminated {
		t.Error("Handle should signal conversation termination")
	}
	if result == nil {
		t.Fatal("Handle returned nil result")
	}
	if done, ok := result["done"].(bool); !ok || !done {
		t.Errorf("result.done = %v, want true", result["done"])
	}
	if reason, ok := result["reason"].(string); !ok || reason != "looks good" {
		t.Errorf("result.reason = %v, want %q", result["reason"], "looks good")
	}
}

func TestSubmitResultTool_TerminatesConversation(t *testing.T) {
	// When submit_result is called, the agent loop should stop.
	// This tests via a mock LLM that calls submit_result on first turn.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	submitArgs := `{"done": true, "reason": "all good"}`

	mockLLM := &mockLLMForSubmitResult{
		response: &provider.GenerateResult{
			Text: "I've completed my analysis.",
			ToolCalls: []provider.ToolCall{
				{
					ID:    "call_1",
					Name:  "submit_result",
					Input: json.RawMessage(submitArgs),
				},
			},
			Usage: provider.Usage{InputTokens: 100, OutputTokens: 50},
		},
	}

	runner := &AgentRunner{model: mockLLM}
	cfg := AgentConfig{
		Description:  "judge",
		ResultSchema: schema,
		MaxTurns:     10,
	}

	toolDef := SubmitResultToolDef(schema)
	result, err := runner.Run(context.Background(), cfg, "Evaluate this", "test-model", []goai.Tool{toolDef})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	// Agent should have terminated after 1 turn (submit_result).
	if result.Turns > 1 {
		t.Errorf("expected 1 turn (submit_result terminates), got %d", result.Turns)
	}
	// Result should be populated from submit_result args.
	if result.Result == nil {
		t.Fatal("AgentResult.Result is nil - submit_result not extracted")
	}
	if done, ok := result.Result["done"].(bool); !ok || !done {
		t.Errorf("Result.done = %v, want true", result.Result["done"])
	}
	if mockLLM.called != 1 {
		t.Errorf("expected LLM called exactly once, got %d", mockLLM.called)
	}
}

// mockLLMForSubmitResult returns a fixed response with a submit_result tool call.
type mockLLMForSubmitResult struct {
	response *provider.GenerateResult
	called   int
}

func (m *mockLLMForSubmitResult) ModelID() string { return "mock-submit-result" }

func (m *mockLLMForSubmitResult) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	m.called++
	if m.called == 1 {
		return m.response, nil
	}
	return &provider.GenerateResult{
		Text:         "done",
		Usage:        provider.Usage{InputTokens: 10, OutputTokens: 5},
		FinishReason: provider.FinishStop,
	}, nil
}

func (m *mockLLMForSubmitResult) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

// 5. TestUntilAgentLoop - untilAgent executor logic

func TestUntilAgentLoop(t *testing.T) {
	// Create a workflow with a loop step that has untilAgent: judge.
	// Mock Model: judge returns result.done=false first iteration, result.done=true second.
	// Assert loop runs exactly 2 iterations.

	wfYAML := `
name: until-agent-test
agents:
  worker:
    description: "Does iterative work"
  judge:
    description: "Evaluates completion"
    resultSchema:
      type: object
      properties:
        done:
          type: boolean
        reason:
          type: string
      required:
        - done
steps:
  - id: iterate
    agent: worker
    instructions: "Iterate on the task"
    loop:
      maxIterations: 5
      untilAgent: judge
`

	wf, err := ParseWorkflow([]byte(wfYAML))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if wf.Steps[0].Loop == nil {
		t.Fatal("step loop is nil")
	}
	if wf.Steps[0].Loop.UntilAgent != "judge" {
		t.Errorf("Loop.UntilAgent = %q, want %q", wf.Steps[0].Loop.UntilAgent, "judge")
	}

	// Mock LLM that tracks iterations:
	// - worker: returns "iteration N" content
	// - judge (via submit_result): returns done=false first, done=true second
	iteration := 0
	workerCalls := 0
	mockLLM := &mockLLMForUntilAgent{
		onCall: func(req provider.GenerateParams) (*provider.GenerateResult, error) {
			// Determine if this is a judge call by checking for submit_result tool.
			hasSubmitResult := false
			for _, tool := range req.Tools {
				if tool.Name == "submit_result" {
					hasSubmitResult = true
					break
				}
			}

			if hasSubmitResult {
				// Judge call
				iteration++
				done := iteration >= 2
				args := map[string]any{
					"done":   done,
					"reason": "iteration check",
				}
				argsJSON, _ := json.Marshal(args)
				return &provider.GenerateResult{
					Text: "Evaluating...",
					ToolCalls: []provider.ToolCall{
						{
							ID:    "call_judge",
							Name:  "submit_result",
							Input: json.RawMessage(argsJSON),
						},
					},
					Usage: provider.Usage{InputTokens: 50, OutputTokens: 25},
				}, nil
			}

			// Worker call
			workerCalls++
			return &provider.GenerateResult{
				Text:  "working...",
				Usage: provider.Usage{InputTokens: 100, OutputTokens: 50},
			}, nil
		},
	}

	executor := &Executor{
		Runner: &AgentRunner{
			model: mockLLM,
		},
		Storage:  NewMemoryStorage(),
		Workflow: wf,
	}

	result, err := executor.Run(context.Background())
	if err != nil {
		t.Fatalf("executor.Run: %v", err)
	}

	// The loop should have run exactly 2 iterations (judge said done=true on 2nd).
	if iteration != 2 {
		t.Errorf("judge was called %d times, want 2", iteration)
	}
	if workerCalls != 2 {
		t.Errorf("expected 2 worker calls, got %d", workerCalls)
	}

	// The overall workflow should complete successfully.
	if result.Status != spec.StatusCompleted {
		t.Errorf("workflow status = %q, want %q", result.Status, spec.StatusCompleted)
	}

	// Verify token accumulation across loop iterations + judge calls.
	// Worker: 2 calls × (100 input, 50 output) = (200, 100)
	// Judge: 2 calls × (50 input, 25 output) = (100, 50)
	// Total for loop step: (300, 150)
	sr, ok := result.Steps["iterate"]
	if !ok {
		t.Fatal("step 'iterate' missing from results")
	}
	wantInput := 300
	wantOutput := 150
	if sr.Tokens.InputTokens != wantInput {
		t.Errorf("step tokens input = %d, want %d (2 worker + 2 judge)", sr.Tokens.InputTokens, wantInput)
	}
	if sr.Tokens.OutputTokens != wantOutput {
		t.Errorf("step tokens output = %d, want %d (2 worker + 2 judge)", sr.Tokens.OutputTokens, wantOutput)
	}

	// Verify workflow-level token aggregation matches step tokens (single-step workflow).
	if result.Tokens.InputTokens != wantInput {
		t.Errorf("workflow tokens input = %d, want %d", result.Tokens.InputTokens, wantInput)
	}
	if result.Tokens.OutputTokens != wantOutput {
		t.Errorf("workflow tokens output = %d, want %d", result.Tokens.OutputTokens, wantOutput)
	}
}

// mockLLMForUntilAgent dispatches based on a callback.
type mockLLMForUntilAgent struct {
	onCall func(provider.GenerateParams) (*provider.GenerateResult, error)
}

func (m *mockLLMForUntilAgent) ModelID() string { return "mock-until-agent" }

func (m *mockLLMForUntilAgent) DoGenerate(_ context.Context, params provider.GenerateParams) (*provider.GenerateResult, error) {
	return m.onCall(params)
}

func (m *mockLLMForUntilAgent) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("not implemented")
}

// 6. TestResultSchemaValidation - runtime validation of submit_result args

func TestResultSchemaValidation_ValidResult(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	handler := NewSubmitResultHandler(schema)

	// Valid input
	args := json.RawMessage(`{"done": true, "reason": "looks good"}`)
	result, terminated, err := handler.Handle(args)
	if err != nil {
		t.Fatalf("Handle valid input: %v", err)
	}
	if !terminated {
		t.Error("should terminate on valid input")
	}
	if result == nil {
		t.Fatal("result is nil for valid input")
	}
}

func TestResultSchemaValidation_MissingRequired(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	handler := NewSubmitResultHandler(schema)

	// Missing required "done" field
	args := json.RawMessage(`{"wrong": "field"}`)
	_, _, err := handler.Handle(args)
	if err == nil {
		t.Fatal("expected validation error for missing required field 'done', got nil")
	}
	if !strings.Contains(err.Error(), "done") {
		t.Errorf("error should mention missing 'done' field, got: %v", err)
	}
}

func TestResultSchemaValidation_WrongType(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	handler := NewSubmitResultHandler(schema)

	// "done" should be boolean, not string
	args := json.RawMessage(`{"done": "yes", "reason": "looks good"}`)
	_, _, err := handler.Handle(args)
	if err == nil {
		t.Fatal("expected validation error for wrong type on 'done', got nil")
	}
}

func TestResultSchemaValidation_FloatAsInteger(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
		},
		"required": []any{"count"},
	}

	handler := NewSubmitResultHandler(schema)

	// 3.7 is not an integer - must reject.
	args := json.RawMessage(`{"count": 3.7}`)
	_, _, err := handler.Handle(args)
	if err == nil {
		t.Fatal("expected validation error for float 3.7 as integer, got nil")
	}

	// 3.0 IS an integer - must accept.
	args = json.RawMessage(`{"count": 3.0}`)
	result, terminated, err := handler.Handle(args)
	if err != nil {
		t.Fatalf("3.0 should be valid integer: %v", err)
	}
	if !terminated {
		t.Error("should terminate on valid input")
	}
	if result["count"] != 3.0 {
		t.Errorf("count = %v, want 3.0", result["count"])
	}
}

func TestResultSchemaValidation_EmptyObject(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":   map[string]any{"type": "boolean"},
			"reason": map[string]any{"type": "string"},
		},
		"required": []any{"done", "reason"},
	}

	handler := NewSubmitResultHandler(schema)

	args := json.RawMessage(`{}`)
	_, _, err := handler.Handle(args)
	if err == nil {
		t.Fatal("expected validation error for empty object with required fields, got nil")
	}
}

// 7. TestInnerStepValidation - inner loop steps must be validated

func TestInnerStepValidation_DuplicateID(t *testing.T) {
	input := `
name: inner-dup
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner1
          agent: w
          instructions: "a"
        - id: inner1
          agent: w
          instructions: "b"
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for duplicate inner step ID")
	}
	var target *DuplicateStepError
	if !errors.As(err, &target) {
		t.Errorf("expected DuplicateStepError, got %T: %v", err, err)
	}
}

func TestInnerStepValidation_BadAgentRef(t *testing.T) {
	input := `
name: inner-bad-agent
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner1
          agent: nonexistent
          instructions: "a"
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for bad agent ref in inner step")
	}
	var target *MissingAgentError
	if !errors.As(err, &target) {
		t.Errorf("expected MissingAgentError, got %T: %v", err, err)
	}
}

func TestInnerStepValidation_BadStepIDPattern(t *testing.T) {
	input := `
name: inner-bad-id
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: "123bad"
          agent: w
          instructions: "a"
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for bad step ID pattern in inner step")
	}
}

func TestInnerStepValidation_BadDepRef(t *testing.T) {
	input := `
name: inner-bad-dep
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner1
          agent: w
          instructions: "a"
          dependsOn: [nonexistent]
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for bad dep ref in inner step")
	}
	var target *MissingDepError
	if !errors.As(err, &target) {
		t.Errorf("expected MissingDepError, got %T: %v", err, err)
	}
}

func TestInnerStepValidation_ForwardDepAllowed(t *testing.T) {
	// Inner step B depends on inner step A, but B is declared first.
	// This should be allowed (same as top-level forward deps).
	input := `
name: inner-forward-dep
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner-b
          agent: w
          instructions: "b depends on a"
          dependsOn: [inner-a]
        - id: inner-a
          agent: w
          instructions: "a runs first"
`
	_, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("forward dep in inner steps should be allowed, got: %v", err)
	}
}

func TestInnerStepValidation_CycleDetected(t *testing.T) {
	input := `
name: inner-cycle
agents:
  w:
    description: "worker"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner-a
          agent: w
          instructions: "a"
          dependsOn: [inner-b]
        - id: inner-b
          agent: w
          instructions: "b"
          dependsOn: [inner-a]
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for cycle in inner steps")
	}
	var target *CycleError
	if !errors.As(err, &target) {
		t.Errorf("expected CycleError, got %T: %v", err, err)
	}
}

// 8. TestUntilAgentSchemaValidation - untilAgent must have resultSchema with done

func TestUntilAgentSchemaValidation_NoResultSchema(t *testing.T) {
	input := `
name: until-no-schema
agents:
  worker:
    description: "worker"
  judge:
    description: "judge without resultSchema"
steps:
  - id: iterate
    agent: worker
    instructions: "work"
    loop:
      maxIterations: 5
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error: untilAgent judge has no resultSchema")
	}
	if !strings.Contains(err.Error(), "resultSchema") {
		t.Errorf("error should mention resultSchema, got: %v", err)
	}
}

func TestUntilAgentSchemaValidation_NoDoneProperty(t *testing.T) {
	input := `
name: until-no-done
agents:
  worker:
    description: "worker"
  judge:
    description: "judge without done"
    resultSchema:
      type: object
      properties:
        reason:
          type: string
      required:
        - reason
steps:
  - id: iterate
    agent: worker
    instructions: "work"
    loop:
      maxIterations: 5
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error: untilAgent judge resultSchema missing done property")
	}
	if !strings.Contains(err.Error(), "done") {
		t.Errorf("error should mention done, got: %v", err)
	}
}

func TestUntilAgentSchemaValidation_DoneNotBoolean(t *testing.T) {
	input := `
name: until-done-not-bool
agents:
  worker:
    description: "worker"
  judge:
    description: "judge with non-boolean done"
    resultSchema:
      type: object
      properties:
        done:
          type: string
      required:
        - done
steps:
  - id: iterate
    agent: worker
    instructions: "work"
    loop:
      maxIterations: 5
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error: untilAgent done must be boolean")
	}
	if !strings.Contains(err.Error(), "boolean") {
		t.Errorf("error should mention boolean, got: %v", err)
	}
}

func TestUntilAgentSchemaValidation_DoneNotRequired(t *testing.T) {
	input := `
name: until-done-not-required
agents:
  worker:
    description: "worker"
  judge:
    description: "judge with done not in required"
    resultSchema:
      type: object
      properties:
        done:
          type: boolean
      required:
        - reason
steps:
  - id: iterate
    agent: worker
    instructions: "work"
    loop:
      maxIterations: 5
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error: done must be in required array")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should mention required, got: %v", err)
	}
}

// 10. TestMinLengthValidation - condition and loop.until reject empty strings

func TestMinLengthValidation_EmptyCondition(t *testing.T) {
	input := `
name: empty-condition
steps:
  - id: step1
    instructions: "work"
    condition: ""
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for empty condition string")
	}
	if !strings.Contains(err.Error(), "condition must not be empty") {
		t.Errorf("error should mention empty condition, got: %v", err)
	}
}

func TestMinLengthValidation_EmptyLoopUntil(t *testing.T) {
	input := `
name: empty-until
agents:
  w:
    description: "worker"
steps:
  - id: step1
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      until: ""
`
	_, err := ParseWorkflow([]byte(input))
	if err == nil {
		t.Fatal("expected error for empty loop.until string")
	}
	if !strings.Contains(err.Error(), "until must not be empty") {
		t.Errorf("error should mention empty until, got: %v", err)
	}
}

func TestMinLengthValidation_NilConditionIsValid(t *testing.T) {
	// Omitted condition (nil) should be valid - only explicit "" is invalid.
	input := `
name: no-condition
steps:
  - id: step1
    instructions: "work"
`
	_, err := ParseWorkflow([]byte(input))
	if err != nil {
		t.Fatalf("omitted condition should be valid, got: %v", err)
	}
}

func intPtr(n int) *int       { return &n }
func strPtr(s string) *string { return &s }
