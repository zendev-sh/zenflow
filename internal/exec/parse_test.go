package exec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testWorkflowYAML = `
name: test-workflow
description: A test workflow
agents:
  coder:
    description: "Writes code"
    prompt: "You are a coder"
    model: gpt-4o
    tools: ["read", "write", "bash", "grep"]
    maxTurns: 30
steps:
  - id: design
    instructions: "Design the system"
  - id: implement
    agent: coder
    dependsOn: [design]
    instructions: "Implement the design"
  - id: review
    dependsOn: [implement]
    instructions: "Review the code"
`

func TestParseWorkflow_Simple(t *testing.T) {
	wf, err := ParseWorkflow([]byte(testWorkflowYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wf.Name != "test-workflow" {
		t.Errorf("name = %q, want %q", wf.Name, "test-workflow")
	}
	if wf.Description != "A test workflow" {
		t.Errorf("description = %q, want %q", wf.Description, "A test workflow")
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(wf.Steps))
	}

	// Verify step IDs.
	ids := []string{"design", "implement", "review"}
	for i, want := range ids {
		if wf.Steps[i].ID != want {
			t.Errorf("step[%d].ID = %q, want %q", i, wf.Steps[i].ID, want)
		}
	}

	// Verify agent config.
	coder, ok := wf.Agents["coder"]
	if !ok {
		t.Fatal("agent 'coder' not found")
	}
	if coder.Model != "gpt-4o" {
		t.Errorf("coder.Model = %q, want %q", coder.Model, "gpt-4o")
	}
	if coder.MaxTurns != 30 {
		t.Errorf("coder.MaxTurns = %d, want 30", coder.MaxTurns)
	}
	wantTools := []string{"read", "write", "bash", "grep"}
	if len(coder.Tools) != len(wantTools) {
		t.Errorf("coder.Tools = %v, want %v", coder.Tools, wantTools)
	} else {
		for i, w := range wantTools {
			if coder.Tools[i] != w {
				t.Errorf("coder.Tools[%d] = %q, want %q", i, coder.Tools[i], w)
			}
		}
	}

	// Verify dependencies.
	if len(wf.Steps[1].DependsOn) != 1 || wf.Steps[1].DependsOn[0] != "design" {
		t.Errorf("implement.DependsOn = %v, want [design]", wf.Steps[1].DependsOn)
	}
	if wf.Steps[1].Agent != "coder" {
		t.Errorf("implement.Agent = %q, want %q", wf.Steps[1].Agent, "coder")
	}
}

func TestParseWorkflow_JSON(t *testing.T) {
	// YAML parser also accepts JSON (YAML is a superset of JSON).
	jsonData := `{
		"name": "json-workflow",
		"steps": [
			{"id": "only_step", "instructions": "do it"}
		]
	}`
	wf, err := ParseWorkflow([]byte(jsonData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Name != "json-workflow" {
		t.Errorf("name = %q, want %q", wf.Name, "json-workflow")
	}
	if len(wf.Steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1", len(wf.Steps))
	}
}

func TestParseWorkflow_MinimalOneStep(t *testing.T) {
	yaml := `
name: minimal
steps:
  - id: step1
    instructions: "do something"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Name != "minimal" {
		t.Errorf("name = %q, want %q", wf.Name, "minimal")
	}
	if len(wf.Steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1", len(wf.Steps))
	}
}

// TestParseWorkflow_VersionDefault verifies that workflows omitting the
// `version:` field receive the schema-declared default (1) via
// ApplyDefaults rather than the Go zero value (0). Without this, callers
// inspecting wf.Version on a parsed workflow would see 0 even though the
// schema documents 1 as the default.
func TestParseWorkflow_VersionDefault(t *testing.T) {
	yaml := `
name: noversion
steps:
  - id: step1
    instructions: "do something"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Version != 1 {
		t.Errorf("Version = %d, want 1 (schema default applied by ApplyDefaults)", wf.Version)
	}
}

func TestParseWorkflow_MissingName(t *testing.T) {
	yaml := `
steps:
  - id: step1
    instructions: "do something"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *MissingNameError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingNameError", err)
	}
}

func TestParseWorkflow_NoSteps(t *testing.T) {
	yaml := `
name: no-steps
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *NoStepsError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *NoStepsError", err)
	}
}

func TestParseWorkflow_DuplicateStepID(t *testing.T) {
	yaml := `
name: dup
steps:
  - id: step1
    instructions: "a"
  - id: step1
    instructions: "b"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *DuplicateStepError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *DuplicateStepError", err)
	}
}

func TestParseWorkflow_InvalidAgentRef(t *testing.T) {
	yaml := `
name: bad-agent
agents:
  coder:
    description: "Writes code"
    prompt: "code"
steps:
  - id: step1
    agent: reviewer
    instructions: "review"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *MissingAgentError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingAgentError", err)
	}
}

func TestParseWorkflow_InvalidDepRef(t *testing.T) {
	yaml := `
name: bad-dep
steps:
  - id: step1
    dependsOn: [nonexistent]
    instructions: "do"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *MissingDepError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingDepError", err)
	}
}

func TestParseWorkflow_CycleDetection(t *testing.T) {
	yaml := `
name: cycle
steps:
  - id: a
    dependsOn: [c]
    instructions: "a"
  - id: b
    dependsOn: [a]
    instructions: "b"
  - id: c
    dependsOn: [b]
    instructions: "c"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *CycleError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *CycleError", err)
	}
}

func TestParseWorkflow_SelfCycle(t *testing.T) {
	yaml := `
name: self-cycle
steps:
  - id: a
    dependsOn: [a]
    instructions: "a"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *CycleError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *CycleError", err)
	}
}

func TestParseWorkflow_WithAgents(t *testing.T) {
	yaml := `
name: with-agents
agents:
  coder:
    description: "Writes code"
    prompt: "You are a coder"
    model: gpt-4o
    tools: ["read", "write"]
    disallowedTools: ["bash"]
    maxTurns: 20
    temperature: 0.7
  reviewer:
    description: "Reviews code"
    prompt: "You review"
    model: claude-4-sonnet
steps:
  - id: code
    agent: coder
    instructions: "code it"
  - id: review
    agent: reviewer
    dependsOn: [code]
    instructions: "review it"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wf.Agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(wf.Agents))
	}
	coder := wf.Agents["coder"]
	if coder.Description != "Writes code" {
		t.Errorf("coder.Description = %q", coder.Description)
	}
	if len(coder.DisallowedTools) != 1 || coder.DisallowedTools[0] != "bash" {
		t.Errorf("coder.DisallowedTools = %v", coder.DisallowedTools)
	}
	if coder.Temperature == nil || *coder.Temperature != 0.7 {
		t.Errorf("coder.Temperature = %v", coder.Temperature)
	}
}

func TestParseWorkflow_DurationParsing(t *testing.T) {
	yaml := `
name: with-timeout
steps:
  - id: step1
    instructions: "do"
    timeout: "30m"
options:
  timeout: "1h"
  stepTimeout: "15m"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Steps[0].Timeout.D() != 30*time.Minute {
		t.Errorf("step timeout = %v, want 30m", wf.Steps[0].Timeout.D())
	}
	if wf.Options.Timeout.D() != time.Hour {
		t.Errorf("workflow timeout = %v, want 1h", wf.Options.Timeout.D())
	}
	if wf.Options.StepTimeout.D() != 15*time.Minute {
		t.Errorf("step timeout = %v, want 15m", wf.Options.StepTimeout.D())
	}
}

func TestParseWorkflow_AtRefNotResolved(t *testing.T) {
	// ParseWorkflow does NOT resolve @ refs - they stay as-is.
	yaml := `
name: at-ref
steps:
  - id: step1
    instructions: "@prompts/design.md"
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wf.Steps[0].Instructions != "@prompts/design.md" {
		t.Errorf("instructions = %q, want %q", wf.Steps[0].Instructions, "@prompts/design.md")
	}
}

func TestLoadWorkflow_ResolvesAtRefs(t *testing.T) {
	// Create temp directory with workflow and ref file.
	dir := t.TempDir()

	refContent := "You are an expert designer."
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "design.md"), []byte(refContent), 0o644); err != nil {
		t.Fatal(err)
	}

	wfYAML := `
name: at-ref
agents:
  designer:
    description: "Designs systems"
    prompt: "@prompts/design.md"
steps:
  - id: step1
    agent: designer
    instructions: "@prompts/design.md"
`
	wfPath := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	wf, err := LoadWorkflow(wfPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Agent prompt should be resolved.
	if wf.Agents["designer"].Prompt != refContent {
		t.Errorf("agent prompt = %q, want %q", wf.Agents["designer"].Prompt, refContent)
	}
	// Step instructions should be resolved.
	if wf.Steps[0].Instructions != refContent {
		t.Errorf("step instructions = %q, want %q", wf.Steps[0].Instructions, refContent)
	}
}

func TestLoadWorkflow_AtRefNotFound(t *testing.T) {
	dir := t.TempDir()
	wfYAML := `
name: missing-ref
steps:
  - id: step1
    instructions: "@missing/file.md"
`
	wfPath := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflow(wfPath)
	if err == nil {
		t.Fatal("expected error for missing @ ref, got nil")
	}
}

func TestParseWorkflow_EmptyStepID(t *testing.T) {
	yaml := `
name: empty-id
steps:
  - id: ""
    instructions: "do"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty step ID, got nil")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *ValidationError", err)
	}
}

func TestParseWorkflow_InvalidOnStepFailure(t *testing.T) {
	yaml := `
name: bad-strategy
steps:
  - id: step1
    instructions: "do"
options:
  onStepFailure: "stop"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid onStepFailure, got nil")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *ValidationError", err)
	}
}

func TestParseWorkflow_ValidOnStepFailure(t *testing.T) {
	for _, strategy := range []string{"cascade", "skip-dependents", "abort"} {
		yaml := `
name: good-strategy
steps:
  - id: step1
    instructions: "do"
options:
  onStepFailure: "` + strategy + `"
`
		_, err := ParseWorkflow([]byte(yaml))
		if err != nil {
			t.Errorf("strategy %q: unexpected error: %v", strategy, err)
		}
	}
}

func TestParseWorkflow_AgentRefNoAgentsMap(t *testing.T) {
	// Step references an agent but no agents section defined.
	yaml := `
name: no-agents
steps:
  - id: step1
    agent: reviewer
    instructions: "review"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for agent ref with no agents map, got nil")
	}
	var target *MissingAgentError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingAgentError", err)
	}
	if target.Agent != "reviewer" {
		t.Errorf("target.Agent = %q, want %q", target.Agent, "reviewer")
	}
	if target.StepID != "step1" {
		t.Errorf("target.StepID = %q, want %q", target.StepID, "step1")
	}
}

func TestParseWorkflow_ErrorStructuredFields(t *testing.T) {
	// Verify that structured error fields are populated.
	yaml := `
name: dup
steps:
  - id: a
    instructions: "do"
  - id: a
    instructions: "dup"
`
	_, err := ParseWorkflow([]byte(yaml))
	var dup *DuplicateStepError
	if !errors.As(err, &dup) {
		t.Fatalf("error type = %T, want *DuplicateStepError", err)
	}
	if dup.StepID != "a" {
		t.Errorf("dup.StepID = %q, want %q", dup.StepID, "a")
	}
}

// --- validateInnerSteps coverage tests ---

func TestValidateInnerSteps_EmptyID(t *testing.T) {
	yaml := `
name: inner-empty-id
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
        - id: ""
          agent: w
          instructions: "inner work"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty inner step ID")
	}
	// The YAML parser with KnownFields might reject empty ID, or validate catches it.
	// Either way, we expect an error.
}

func TestValidateInnerSteps_IncludeMutualExclusion(t *testing.T) {
	// Inner step with include + agent should be rejected.
	yaml := `
name: inner-include-conflict
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
          include: "other.yaml"
          agent: w
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step include + agent")
	}
	var target *IncludeConflictError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *IncludeConflictError, err = %v", err, err)
	}
}

func TestValidateInnerSteps_IncludeWithInstructions(t *testing.T) {
	yaml := `
name: inner-include-instructions
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
          include: "other.yaml"
          instructions: "conflicting"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step include + instructions")
	}
	var target *IncludeConflictError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *IncludeConflictError", err)
	}
}

func TestValidateInnerSteps_IncludeWithCondition(t *testing.T) {
	cond := "true"
	_ = cond
	yaml := `
name: inner-include-condition
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
          include: "other.yaml"
          condition: "true"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step include + condition")
	}
	var target *IncludeConflictError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *IncludeConflictError", err)
	}
}

func TestValidateInnerSteps_IncludeWithContextFiles(t *testing.T) {
	yaml := `
name: inner-include-ctxfiles
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
          include: "other.yaml"
          contextFiles: ["a.txt"]
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step include + contextFiles")
	}
	var target *IncludeConflictError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *IncludeConflictError", err)
	}
}

func TestValidateInnerSteps_IncludeWithModel(t *testing.T) {
	yaml := `
name: inner-include-model
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
          include: "other.yaml"
          model: "gpt-4o"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step include + model")
	}
	var target *IncludeConflictError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *IncludeConflictError", err)
	}
}

func TestValidateInnerSteps_MissingAgentRef(t *testing.T) {
	yaml := `
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
          instructions: "inner work"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step referencing unknown agent")
	}
	var target *MissingAgentError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingAgentError", err)
	}
}

func TestValidateInnerSteps_BadDepRef(t *testing.T) {
	yaml := `
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
          instructions: "inner work"
          dependsOn: [nonexistent]
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step with bad dependency")
	}
	var target *MissingDepError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingDepError", err)
	}
}

func TestValidateInnerSteps_DuplicateID(t *testing.T) {
	yaml := `
name: inner-dup-id
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
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for duplicate inner step ID")
	}
	var target *DuplicateStepError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *DuplicateStepError", err)
	}
}

func TestValidateInnerSteps_NestedLoop(t *testing.T) {
	yaml := `
name: inner-nested-loop
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
          instructions: "inner"
          loop:
            maxIterations: 2
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for nested loop")
	}
	var target *LoopValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *LoopValidationError", err)
	}
}

func TestValidateInnerSteps_InvalidIDPattern(t *testing.T) {
	yaml := `
name: inner-bad-pattern
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
        - id: "123invalid"
          agent: w
          instructions: "inner"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid inner step ID pattern")
	}
}

// --- validateUntilAgentSchema coverage tests ---

func TestValidateUntilAgentSchema_PropertiesNotMap(t *testing.T) {
	yaml := `
name: bad-props
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties: "not-a-map"
      required: [done]
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for properties not a map")
	}
	if !strings.Contains(err.Error(), "must have properties") {
		t.Errorf("error = %v, want 'must have properties'", err)
	}
}

func TestValidateUntilAgentSchema_DoneNotObject(t *testing.T) {
	yaml := `
name: done-not-obj
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done: "not-an-object"
      required: [done]
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for done not an object")
	}
	if !strings.Contains(err.Error(), "done must be an object") {
		t.Errorf("error = %v, want 'done must be an object'", err)
	}
}

func TestValidateUntilAgentSchema_RequiredNotArray(t *testing.T) {
	yaml := `
name: req-not-array
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done:
          type: boolean
      required: "not-an-array"
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for required not an array")
	}
	if !strings.Contains(err.Error(), "required must be an array") {
		t.Errorf("error = %v, want 'required must be an array'", err)
	}
}

func TestValidateUntilAgentSchema_DoneMissing(t *testing.T) {
	yaml := `
name: done-missing
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        result:
          type: string
      required: [result]
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing done property")
	}
	if !strings.Contains(err.Error(), "must have properties.done") {
		t.Errorf("error = %v, want 'must have properties.done'", err)
	}
}

func TestValidateUntilAgentSchema_DoneWrongType(t *testing.T) {
	yaml := `
name: done-wrong-type
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done:
          type: string
      required: [done]
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for done wrong type")
	}
	if !strings.Contains(err.Error(), "type boolean") {
		t.Errorf("error = %v, want 'type boolean'", err)
	}
}

func TestValidateUntilAgentSchema_DoneNotInRequired(t *testing.T) {
	yaml := `
name: done-not-required
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done:
          type: boolean
      required: [something_else]
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for done not in required")
	}
	if !strings.Contains(err.Error(), "'done' in required array") {
		t.Errorf("error = %v, want \"'done' in required array\"", err)
	}
}

// --- toAnySlice coverage ([]string path) ---

func TestToAnySlice_StringSlice(t *testing.T) {
	// Directly test toAnySlice with []string input.
	input := []string{"a", "b", "c"}
	result, ok := toAnySlice(input)
	if !ok {
		t.Fatal("expected ok=true for []string")
	}
	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	for i, want := range []string{"a", "b", "c"} {
		if s, ok := result[i].(string); !ok || s != want {
			t.Errorf("result[%d] = %v, want %q", i, result[i], want)
		}
	}
}

func TestToAnySlice_AnySlice(t *testing.T) {
	input := []any{"x", "y"}
	result, ok := toAnySlice(input)
	if !ok {
		t.Fatal("expected ok=true for []any")
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
}

func TestToAnySlice_InvalidType(t *testing.T) {
	_, ok := toAnySlice("not-a-slice")
	if ok {
		t.Fatal("expected ok=false for string input")
	}
}

// --- Coverage gap tests ---

func TestLoadWorkflow_FileNotFound(t *testing.T) {
	_, err := LoadWorkflow("/nonexistent/path/workflow.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *ValidationError", err)
	}
}

func TestLoadWorkflow_InvalidYAMLFile(t *testing.T) {
	// LoadWorkflow → ParseWorkflow fails → returns error (covers L24-26).
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("not: [valid: yaml: {"), 0o644)
	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadWorkflow_AgentPromptRefError(t *testing.T) {
	// Covers resolveRefs agent prompt error path (L273-275).
	dir := t.TempDir()
	content := []byte("name: test\nagents:\n  a:\n    description: test\n    prompt: \"@missing.md\"\nsteps:\n  - id: s1\n    agent: a\n")
	path := filepath.Join(dir, "wf.yaml")
	os.WriteFile(path, content, 0o644)
	_, err := LoadWorkflow(path)
	if err == nil {
		t.Fatal("expected error for missing agent prompt ref")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *ValidationError", err)
	}
	if !strings.Contains(err.Error(), "agent a prompt") {
		t.Errorf("error = %v, want to mention agent prompt", err)
	}
}

func TestParseWorkflow_InvalidYAML(t *testing.T) {
	// Covers YAML decode error path (L43-45).
	_, err := ParseWorkflow([]byte("not: [valid: yaml: {"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	var target *ValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *ValidationError", err)
	}
}

func TestParseWorkflow_AgentNegativeMaxTurns(t *testing.T) {
	yaml := `
name: neg-maxturns
agents:
  a:
    description: "test"
    maxTurns: -1
steps:
  - id: s1
    agent: a
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for negative maxTurns")
	}
	if !strings.Contains(err.Error(), "maxTurns must be non-negative") {
		t.Errorf("error = %v, want maxTurns validation", err)
	}
}

func TestParseWorkflow_AgentTemperatureOutOfRange(t *testing.T) {
	yaml := `
name: bad-temp
agents:
  a:
    description: "test"
    temperature: 3.0
steps:
  - id: s1
    agent: a
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for temperature > 2")
	}
	if !strings.Contains(err.Error(), "temperature must be between 0 and 2") {
		t.Errorf("error = %v, want temperature validation", err)
	}
}

func TestParseWorkflow_AgentTopPOutOfRange(t *testing.T) {
	yaml := `
name: bad-topp
agents:
  a:
    description: "test"
    topP: 1.5
steps:
  - id: s1
    agent: a
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for topP > 1")
	}
	if !strings.Contains(err.Error(), "topP must be between 0 and 1") {
		t.Errorf("error = %v, want topP validation", err)
	}
}

// Fix #3: outputMode is optional; valid values are "" (default → "last"),
// "last", and "cumulative". Anything else is a validation error.
func TestParseWorkflow_LoopOutputMode_Valid(t *testing.T) {
	for _, mode := range []string{"", "last", "cumulative"} {
		t.Run("mode="+mode, func(t *testing.T) {
			yaml := `
name: outputmode-valid
agents:
  w:
    description: "worker"
steps:
  - id: s1
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      outputMode: ` + mode + `
`
			_, err := ParseWorkflow([]byte(yaml))
			if err != nil {
				t.Fatalf("unexpected validation error for outputMode=%q: %v", mode, err)
			}
		})
	}
}

func TestParseWorkflow_LoopOutputMode_Invalid(t *testing.T) {
	yaml := `
name: outputmode-invalid
agents:
  w:
    description: "worker"
steps:
  - id: s1
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      outputMode: bogus
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid loop outputMode")
	}
	if !strings.Contains(err.Error(), "invalid loop.outputMode") {
		t.Errorf("error = %v, want invalid loop.outputMode message", err)
	}
}

func TestParseWorkflow_LoopMaxConcurrencyNegative(t *testing.T) {
	yaml := `
name: neg-loop-conc
agents:
  w:
    description: "worker"
steps:
  - id: s1
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      maxConcurrency: -1
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for negative loop maxConcurrency")
	}
	if !strings.Contains(err.Error(), "loop maxConcurrency must be non-negative") {
		t.Errorf("error = %v, want maxConcurrency validation", err)
	}
}

func TestParseWorkflow_ForEachInvalidType(t *testing.T) {
	yaml := `
name: bad-foreach-type
agents:
  w:
    description: "worker"
steps:
  - id: s1
    agent: w
    instructions: "work"
    loop:
      forEach:
        key: value
      steps:
        - id: inner1
          instructions: "inner"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for forEach as map")
	}
	var target *LoopValidationError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *LoopValidationError", err)
	}
	if !strings.Contains(err.Error(), "forEach must be string or array") {
		t.Errorf("error = %v, want forEach type validation", err)
	}
}

func TestParseWorkflow_NegativeRetries(t *testing.T) {
	yaml := `
name: neg-retries
steps:
  - id: s1
    instructions: "work"
    retries: -1
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for negative retries")
	}
	if !strings.Contains(err.Error(), "retries must be non-negative") {
		t.Errorf("error = %v, want retries validation", err)
	}
}

func TestParseWorkflow_UntilAgentNoAgentsMap(t *testing.T) {
	// Covers untilAgent with no agents defined (L194-196).
	yaml := `
name: no-agents-until
steps:
  - id: s1
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for untilAgent with no agents map")
	}
	if !strings.Contains(err.Error(), "no agents defined") {
		t.Errorf("error = %v, want 'no agents defined'", err)
	}
}

func TestValidateInnerSteps_AgentRefNoAgentsMap(t *testing.T) {
	// Covers inner step agent ref when agents map is nil (L323-325).
	yaml := `
name: inner-no-agents
steps:
  - id: outer
    instructions: "work"
    loop:
      maxIterations: 3
      steps:
        - id: inner1
          agent: nonexistent
          instructions: "inner work"
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for inner step agent ref with no agents map")
	}
	var target *MissingAgentError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *MissingAgentError", err)
	}
	if !strings.Contains(err.Error(), "no agents defined") {
		t.Errorf("error = %v, want 'no agents defined'", err)
	}
}

func TestValidateUntilAgentSchema_NoRequiredField(t *testing.T) {
	// Covers L387-389: resultSchema has properties.done but no 'required' key at all.
	yaml := `
name: no-required
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done:
          type: boolean
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "'done' in required array") {
		t.Errorf("error = %v, want \"'done' in required array\"", err)
	}
}

func TestValidateUntilAgentSchema_RequiredNotArrayType(t *testing.T) {
	// Covers toAnySlice returning false in validateUntilAgentSchema (L390-392).
	// Use required: 42 (integer, not array or string slice).
	yaml := `
name: req-int
agents:
  w:
    description: "worker"
  judge:
    description: "judge"
    resultSchema:
      properties:
        done:
          type: boolean
      required: 42
steps:
  - id: outer
    agent: w
    instructions: "work"
    loop:
      maxIterations: 3
      untilAgent: judge
`
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for required as integer")
	}
	if !strings.Contains(err.Error(), "required must be an array") {
		t.Errorf("error = %v, want 'required must be an array'", err)
	}
}

func TestResolveRefs_PathTraversal(t *testing.T) {
	wf := &Workflow{
		Name: "test",
		Agents: map[string]AgentConfig{
			"a": {Description: "test", Prompt: "@../../etc/passwd"},
		},
		Steps: []Step{{ID: "s1", Agent: "a"}},
	}
	err := resolveRefs(wf, t.TempDir())
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error should mention 'escapes': %v", err)
	}
}

func TestReadRef_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := readRef(dir, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected path traversal error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' in error, got: %v", err)
	}
}

func TestReadRef_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := readRef(dir, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected file not found error")
	}
}

func TestReadRef_ValidFile(t *testing.T) {
	dir := t.TempDir()
	content := "hello world"
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readRef(dir, "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestReadRef_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	// Create a symlink that points outside the base dir.
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	_, err := readRef(dir, "link.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' in error, got: %v", err)
	}
}
