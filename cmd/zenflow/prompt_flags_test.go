package main

import (
	"strings"
	"testing"

	"github.com/zendev-sh/goai/provider"
)

// The --system-prompt / --skills flags are validated before any LLM call.
// Each command must exit 3 with a clear message when the prompt file or the
// skills directory cannot be read, rather than silently dropping the context.

func TestCLI_FlowSystemPromptMissingFile(t *testing.T) {
	path := writeWorkflow(t, "name: test\nsteps:\n  - id: s1\n    instructions: hi\n")
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock,
		"zenflow", "flow", path, "--no-dotenv", "--system-prompt", "@/no/such/file")
	defer cleanup()
	cmdFlow()
	if env.exitCode != 3 {
		t.Fatalf("exit = %d, want 3; stderr=%s", env.exitCode, env.stderr.String())
	}
	if !strings.Contains(env.stderr.String(), "--system-prompt") {
		t.Errorf("stderr = %q, want --system-prompt error", env.stderr.String())
	}
}

func TestCLI_GoalSystemPromptMissingFile(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock,
		"zenflow", "goal", "do X", "--no-dotenv", "--system-prompt", "@/no/such/file")
	defer cleanup()
	cmdGoal()
	if env.exitCode != 3 {
		t.Fatalf("exit = %d, want 3; stderr=%s", env.exitCode, env.stderr.String())
	}
	if !strings.Contains(env.stderr.String(), "--system-prompt") {
		t.Errorf("stderr = %q, want --system-prompt error", env.stderr.String())
	}
}

func TestCLI_AgentSkillsMissingDir(t *testing.T) {
	mock := &mockLLM{resp: &provider.GenerateResult{Text: "ok"}}
	env, cleanup := setupTestWithLLM(t, mock,
		"zenflow", "agent", "hi", "--no-dotenv", "--skills", "/no/such/skills")
	defer cleanup()
	cmdAgent()
	if env.exitCode != 3 {
		t.Fatalf("exit = %d, want 3; stderr=%s", env.exitCode, env.stderr.String())
	}
	if !strings.Contains(env.stderr.String(), "skills:") {
		t.Errorf("stderr = %q, want skills error", env.stderr.String())
	}
}
