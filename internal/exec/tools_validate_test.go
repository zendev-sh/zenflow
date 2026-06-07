package exec

import (
	"strings"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

// TestValidateToolNames_NoDuplicates verifies that a set of tools with
// unique names passes validation without error.
func TestValidateToolNames_NoDuplicates(t *testing.T) {
	tools := []goai.Tool{
		makeTool("read_file", "read a file", "content"),
		makeTool("write_file", "write a file", "ok"),
		makeTool("list_dir", "list a directory", "[]"),
	}
	if err := validateToolNames(tools); err != nil {
		t.Errorf("unexpected error for unique tools: %v", err)
	}
}

// TestValidateToolNames_Duplicate verifies that two tools sharing the same
// name via WithRunnerTools return an error containing "duplicate tool name".
func TestValidateToolNames_Duplicate(t *testing.T) {
	tools := []goai.Tool{
		makeTool("read_file", "read a file", "content"),
		makeTool("read_file", "another read_file", "content2"),
	}
	err := validateToolNames(tools)
	if err == nil {
		t.Fatal("expected error for duplicate tool name, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate tool name") {
		t.Errorf("error %q does not contain 'duplicate tool name'", err.Error())
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("error %q does not mention the duplicate name 'read_file'", err.Error())
	}
}

// TestValidateToolNames_Empty verifies that an empty tool slice passes validation.
func TestValidateToolNames_Empty(t *testing.T) {
	if err := validateToolNames(nil); err != nil {
		t.Errorf("unexpected error for nil tools: %v", err)
	}
	if err := validateToolNames([]goai.Tool{}); err != nil {
		t.Errorf("unexpected error for empty tools: %v", err)
	}
}

// TestAgentRunner_Run_DuplicateToolNames verifies that AgentRunner.Run returns
// an error containing "duplicate tool name" when two tools with the same name
// are supplied via WithRunnerTools.
func TestAgentRunner_Run_DuplicateToolNames(t *testing.T) {
	model := &mockModel{
		responses: []*provider.GenerateResult{
			textResult("should not reach", 10, 5),
		},
	}
	dupTools := []goai.Tool{
		makeTool("my_tool", "first instance", "result-a"),
		makeTool("my_tool", "second instance", "result-b"),
	}
	runner := &AgentRunner{model: model}
	_, err := runner.Run(t.Context(), AgentConfig{}, "do something", "gpt-4o", dupTools)
	if err == nil {
		t.Fatal("expected error for duplicate tool names, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate tool name") {
		t.Errorf("error %q does not contain 'duplicate tool name'", err.Error())
	}
}
