package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExpandEnvVars covers the substitution scanner: ${VAR}/$VAR forms,
// unset -> empty, the $$ escape, and the literals it must leave untouched.
func TestExpandEnvVars(t *testing.T) {
	t.Setenv("ZF_HOST", "example.com")
	t.Setenv("ZF_EMPTY", "")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no dollar fast path", "plain text", "plain text"},
		{"braced", "https://${ZF_HOST}/feed", "https://example.com/feed"},
		{"bare", "host is $ZF_HOST done", "host is example.com done"},
		{"bare at end", "host=$ZF_HOST", "host=example.com"},
		{"set but empty", "[${ZF_EMPTY}]", "[]"},
		{"unset braced -> empty", "x${ZF_MISSING}y", "xy"},
		{"unset bare -> empty", "x $ZF_MISSING y", "x  y"},
		{"escape braced", "cost $${ZF_HOST}", "cost ${ZF_HOST}"},
		{"escape bare", "price $$5 each", "price $5 each"},
		{"dollar digit literal", "save $5 now", "save $5 now"},
		{"dollar space literal", "a $ b", "a $ b"},
		{"trailing dollar literal", "ends with $", "ends with $"},
		{"unterminated brace literal", "a ${ZF_HOST", "a ${ZF_HOST"},
		{"non-name brace literal", "a ${1BAD} b", "a ${1BAD} b"},
		{"invalid char mid-name literal", "a ${A-B} c", "a ${A-B} c"},
		{"empty brace literal", "a ${} b", "a ${} b"},
		{"multiple", "$ZF_HOST/${ZF_HOST}", "example.com/example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandEnvVars(tc.in); got != tc.want {
				t.Errorf("expandEnvVars(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExpandWorkflowEnv verifies interpolation reaches description, agent
// prompts, top-level instructions and loop-nested instructions, while
// leaving IDs and toolInput ($-reserved for CEL) untouched.
func TestExpandWorkflowEnv(t *testing.T) {
	t.Setenv("ZF_URL", "https://example.com")
	t.Setenv("ZF_TOPIC", "otters")

	mi := 3
	wf := &Workflow{
		Name:        "wf",
		Description: "scrape ${ZF_URL}",
		Agents: map[string]AgentConfig{
			"writer": {Description: "d", Prompt: "write about ${ZF_TOPIC}"},
		},
		Steps: []Step{
			{ID: "a", Instructions: "fetch ${ZF_URL}"},
			{ID: "b", Loop: &Loop{
				MaxIterations: &mi,
				Steps:         []Step{{ID: "inner", Instructions: "loop ${ZF_TOPIC}"}},
			}},
			// toolInput must NOT be expanded ($ is CEL there).
			{ID: "c", Tool: "read", ToolInput: map[string]any{"path": "${ZF_URL}"}},
		},
	}
	expandWorkflowEnv(wf)

	if wf.Description != "scrape https://example.com" {
		t.Errorf("description = %q", wf.Description)
	}
	if got := wf.Agents["writer"].Prompt; got != "write about otters" {
		t.Errorf("agent prompt = %q", got)
	}
	if wf.Steps[0].Instructions != "fetch https://example.com" {
		t.Errorf("step a = %q", wf.Steps[0].Instructions)
	}
	if got := wf.Steps[1].Loop.Steps[0].Instructions; got != "loop otters" {
		t.Errorf("inner step = %q", got)
	}
	if got := wf.Steps[2].ToolInput["path"]; got != "${ZF_URL}" {
		t.Errorf("toolInput must be untouched, got %v", got)
	}
}

// TestParseWorkflow_EnvInterpolation is the end-to-end parse path: a ${VAR}
// in YAML instructions is resolved by ParseWorkflow.
func TestParseWorkflow_EnvInterpolation(t *testing.T) {
	t.Setenv("ZF_FEED", "https://rss.example.com/feed.xml")
	yaml := `
name: feeds
steps:
  - id: scrape
    instructions: "Scrape ${ZF_FEED} and summarize."
`
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	if !strings.Contains(wf.Steps[0].Instructions, "https://rss.example.com/feed.xml") {
		t.Errorf("instructions not interpolated: %q", wf.Steps[0].Instructions)
	}
}

// TestLoadWorkflow_EnvInRefPathAndContent covers the @-ref path: a ${VAR}
// in the ref path resolves the file, and ${VAR} inside the referenced file
// is interpolated too.
func TestLoadWorkflow_EnvInRefPathAndContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZF_REF", "prompt.md")
	t.Setenv("ZF_NAME", "Ada")

	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("Hello ${ZF_NAME}, summarize."), 0o600); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	// The instructions reference the file via an env-built (relative) path.
	wfYAML := "name: x\nversion: 1\nsteps:\n  - id: s\n    instructions: \"@${ZF_REF}\"\n"
	wfPath := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	wf, err := LoadWorkflow(wfPath)
	if err != nil {
		t.Fatalf("LoadWorkflow: %v", err)
	}
	if wf.Steps[0].Instructions != "Hello Ada, summarize." {
		t.Errorf("ref content not interpolated: %q", wf.Steps[0].Instructions)
	}
}

// TestLoadWorkflow_EnvInAgentPromptRef covers @-ref expansion on an agent
// prompt loaded from a file.
func TestLoadWorkflow_EnvInAgentPromptRef(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZF_NAME", "Bob")
	if err := os.WriteFile(filepath.Join(dir, "sys.md"), []byte("You are ${ZF_NAME}."), 0o600); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	wfYAML := "name: x\nversion: 1\nagents:\n  w:\n    description: d\n    prompt: \"@sys.md\"\nsteps:\n  - id: s\n    agent: w\n    instructions: go\n"
	wfPath := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	wf, err := LoadWorkflow(wfPath)
	if err != nil {
		t.Fatalf("LoadWorkflow: %v", err)
	}
	if got := wf.Agents["w"].Prompt; got != "You are Bob." {
		t.Errorf("agent prompt ref not interpolated: %q", got)
	}
}

// TestParseWorkflowJSON_EnvInterpolation covers the JSON parse path.
func TestParseWorkflowJSON_EnvInterpolation(t *testing.T) {
	t.Setenv("ZF_FEED", "https://rss.example.com/feed.xml")
	js := `{"name":"feeds","steps":[{"id":"scrape","instructions":"Scrape ${ZF_FEED}."}]}`
	wf, err := ParseWorkflowJSON([]byte(js))
	if err != nil {
		t.Fatalf("ParseWorkflowJSON: %v", err)
	}
	if !strings.Contains(wf.Steps[0].Instructions, "https://rss.example.com/feed.xml") {
		t.Errorf("instructions not interpolated: %q", wf.Steps[0].Instructions)
	}
}
