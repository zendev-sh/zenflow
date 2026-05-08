// Package zenflow - parser-security tests.
// Each test pins one attack the parser is now expected to reject:
// - bidi-override + control characters in user-facing fields (F14)
// - hostile agent map keys (path traversal / shell metacharacters) (F13)
// - chained @-references that nest beyond MaxNestingDepth (F12)
package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- F14 - SanitizeUnicode in ParseWorkflow ------------------------------

// TestParseWorkflow_BidiOverrideRejectedInDescription verifies the
// parser rejects U+202E inside Workflow.Description. The literal byte
// must NEVER reach a downstream consumer where it could mis-render the
// flow as "rm -rf /" while displaying "echo hi".
func TestParseWorkflow_BidiOverrideRejectedInDescription(t *testing.T) {
	yaml := "name: x\n" +
		"description: \"hello\\u202E rm -rf\"\n" +
		"version: 1\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    instructions: ok\n"
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("ParseWorkflow accepted bidi-override in description; want rejection")
	}
	if !strings.Contains(err.Error(), "unicode-unsafe") {
		t.Errorf("err=%v; want 'unicode-unsafe' substring", err)
	}
}

// TestParseWorkflow_BidiOverrideInInstructions covers Step.Instructions.
// Same threat model - a workflow author could hide a destructive command
// inside what visually appears to be safe text.
func TestParseWorkflow_BidiOverrideInInstructions(t *testing.T) {
	yaml := "name: x\n" +
		"version: 1\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    instructions: \"do\\u202Eevil\"\n"
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("ParseWorkflow accepted bidi-override in instructions")
	}
	if !strings.Contains(err.Error(), "unicode-unsafe") {
		t.Errorf("err=%v; want unicode-unsafe", err)
	}
}

// TestParseWorkflow_BidiInAgentPrompt covers AgentConfig.Prompt.
func TestParseWorkflow_BidiInAgentPrompt(t *testing.T) {
	yaml := "name: x\n" +
		"version: 1\n" +
		"agents:\n" +
		"  helper:\n" +
		"    description: ok\n" +
		"    prompt: \"sys\\u202Eevil\"\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    agent: helper\n" +
		"    instructions: ok\n"
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("ParseWorkflow accepted bidi-override in agent.prompt")
	}
	if !strings.Contains(err.Error(), "unicode-unsafe") {
		t.Errorf("err=%v; want unicode-unsafe", err)
	}
}

// TestParseWorkflow_ControlCharStripped verifies that NON-bidi C0
// controls are SILENTLY STRIPPED (not rejected). LF and TAB stay so
// multi-line YAML block scalars survive; bell / vertical-tab / etc.
// disappear.
func TestParseWorkflow_ControlCharStripped(t *testing.T) {
	yaml := "name: x\n" +
		"version: 1\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    instructions: \"hi\\x07world\"\n" // BEL
	wf, err := ParseWorkflow([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseWorkflow rejected BEL char; want strip + accept: %v", err)
	}
	if strings.ContainsRune(wf.Steps[0].Instructions, 0x07) {
		t.Errorf("BEL leaked through: %q", wf.Steps[0].Instructions)
	}
	if !strings.Contains(wf.Steps[0].Instructions, "hi") || !strings.Contains(wf.Steps[0].Instructions, "world") {
		t.Errorf("text trimmed too aggressively: %q", wf.Steps[0].Instructions)
	}
}

// TestParseWorkflow_CleanASCIIPasses regression-tests that the
// sanitisation step does not break the happy path.
func TestParseWorkflow_CleanASCIIPasses(t *testing.T) {
	yaml := "name: clean\n" +
		"description: hi\n" +
		"version: 1\n" +
		"steps:\n  - id: a\n    instructions: do it\n"
	if _, err := ParseWorkflow([]byte(yaml)); err != nil {
		t.Fatalf("clean YAML rejected: %v", err)
	}
}

// --- F13 - agent map keys validated --------------------------------------

// TestParseWorkflow_AgentKeyTraversalRejected proves the parser
// rejects an agent name like `../../etc/passwd`. Without this check
// the key could escape any context that interpolates agent identity
// into a file path or shell command.
func TestParseWorkflow_AgentKeyTraversalRejected(t *testing.T) {
	yaml := "name: x\n" +
		"version: 1\n" +
		"agents:\n" +
		"  ../../etc/passwd:\n" +
		"    description: ok\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    instructions: ok\n"
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("ParseWorkflow accepted traversal agent name")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Errorf("err=%v; want 'must match' validation", err)
	}
}

// TestParseWorkflow_AgentKeyShellMetacharRejected covers a second
// hostile shape: shell metacharacters (newline + rm).
func TestParseWorkflow_AgentKeyShellMetacharRejected(t *testing.T) {
	yaml := "name: x\n" +
		"version: 1\n" +
		"agents:\n" +
		"  \"agent\\nrm -rf /\":\n" +
		"    description: ok\n" +
		"steps:\n" +
		"  - id: a\n" +
		"    instructions: ok\n"
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("ParseWorkflow accepted shell-metachar agent name")
	}
}

// TestParseWorkflow_AgentKeyValidPasses confirms the strict pattern
// still accepts the canonical lower-snake / lower-dash forms used by
// every real workflow.
func TestParseWorkflow_AgentKeyValidPasses(t *testing.T) {
	for _, key := range []string{"helper", "fact-check", "agent_1", "a"} {
		yaml := "name: x\nversion: 1\nagents:\n  " + key + ":\n    description: ok\nsteps:\n  - id: s\n    instructions: ok\n"
		if _, err := ParseWorkflow([]byte(yaml)); err != nil {
			t.Errorf("valid key %q rejected: %v", key, err)
		}
	}
}

// --- F12 - include/ref nesting depth -------------------------------------

// TestResolveRefs_DepthAccept builds 20 chained `@`-files and verifies
// LoadWorkflow walks the chain successfully. The chain length matches
// MaxNestingDepth exactly - the boundary case.
func TestResolveRefs_DepthAccept(t *testing.T) {
	dir := t.TempDir()
	// chain[20].txt = "TERMINAL"
	// chain[i].txt = "@chain<i+1>.txt"
	depth := MaxNestingDepth // 20
	terminalName := "chain_terminal.txt"
	if err := os.WriteFile(filepath.Join(dir, terminalName), []byte("TERMINAL"), 0o600); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	prev := terminalName
	for i := depth - 1; i >= 1; i-- {
		name := chainName(i)
		body := "@" + prev
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write chain %d: %v", i, err)
		}
		prev = name
	}
	// Workflow points at the depth=1 entry.
	wfPath := filepath.Join(dir, "wf.yaml")
	wfYAML := "name: x\nversion: 1\nsteps:\n  - id: s\n    instructions: \"@" + prev + "\"\n"
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	wf, err := LoadWorkflow(wfPath)
	if err != nil {
		t.Fatalf("LoadWorkflow at depth %d unexpectedly failed: %v", depth, err)
	}
	if !strings.Contains(wf.Steps[0].Instructions, "TERMINAL") {
		t.Errorf("instructions did not resolve to terminal text: %q", wf.Steps[0].Instructions)
	}
}

// TestResolveRefs_DepthRejectExceeds builds a chain of MaxNestingDepth+1
// references and verifies LoadWorkflow rejects with a clear error.
func TestResolveRefs_DepthRejectExceeds(t *testing.T) {
	dir := t.TempDir()
	depth := MaxNestingDepth + 1 // 21
	terminalName := "chain_terminal.txt"
	if err := os.WriteFile(filepath.Join(dir, terminalName), []byte("TERMINAL"), 0o600); err != nil {
		t.Fatalf("write terminal: %v", err)
	}
	prev := terminalName
	for i := depth - 1; i >= 1; i-- {
		name := chainName(i)
		body := "@" + prev
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write chain %d: %v", i, err)
		}
		prev = name
	}
	wfPath := filepath.Join(dir, "wf.yaml")
	wfYAML := "name: x\nversion: 1\nsteps:\n  - id: s\n    instructions: \"@" + prev + "\"\n"
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	_, err := LoadWorkflow(wfPath)
	if err == nil {
		t.Fatal("LoadWorkflow accepted chain deeper than MaxNestingDepth")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("err=%v; want 'nesting depth' substring", err)
	}
}

func chainName(i int) string {
	return "chain_" + itoaInt(i) + ".txt"
}

// TestParseWorkflowJSON_BidiOverrideRejected covers the
// SanitizeWorkflowUnicode error-return path in ParseWorkflowJSON. The
// JSON entrypoint must reject bidi-overrides symmetrically with
// ParseWorkflow (YAML), otherwise a coordinator emitting JSON could
// smuggle hostile glyphs into downstream consumers.
func TestParseWorkflowJSON_BidiOverrideRejected(t *testing.T) {
	// U+202E RIGHT-TO-LEFT OVERRIDE embedded in the workflow name.
	jsonBody := "{\"name\":\"bad\u202Ename\",\"version\":1," +
		"\"steps\":[{\"id\":\"a\",\"instructions\":\"ok\"}]}"
	_, err := ParseWorkflowJSON([]byte(jsonBody))
	if err == nil {
		t.Fatal("ParseWorkflowJSON accepted bidi-override in name; want rejection")
	}
	if !strings.Contains(err.Error(), "unicode-unsafe") {
		t.Errorf("err=%v; want 'unicode-unsafe' substring", err)
	}
	if !strings.Contains(err.Error(), "workflow name") {
		t.Errorf("err=%v; want 'workflow name' prefix from SanitizeWorkflowUnicode", err)
	}
}

// TestParseWorkflow_BidiOverrideInStepID covers the step-ID sanitize
// branch in SanitizeWorkflowUnicode. Step IDs must also reject bidi
// overrides; they flow into trace IDs, logs, and serialized outputs.
// We use ParseWorkflowJSON because YAML step IDs are plain scalars that
// the stepIDPattern regex rejects before SanitizeUnicode runs; JSON
// lets us embed the raw U+202E rune directly so the sanitize call is
// what fails.
func TestParseWorkflow_BidiOverrideInStepID(t *testing.T) {
	jsonBody := "{\"name\":\"ok\",\"version\":1," +
		"\"steps\":[{\"id\":\"a\u202Eb\",\"instructions\":\"ok\"}]}"
	_, err := ParseWorkflowJSON([]byte(jsonBody))
	if err == nil {
		t.Fatal("ParseWorkflowJSON accepted bidi-override in step id; want rejection")
	}
	if !strings.Contains(err.Error(), "unicode-unsafe") {
		t.Errorf("err=%v; want 'unicode-unsafe' substring", err)
	}
	if !strings.Contains(err.Error(), "step[0] id") {
		t.Errorf("err=%v; want 'step[0] id' prefix from SanitizeWorkflowUnicode", err)
	}
}

// TestResolveChainedRef_EmptyChainedRefRejected covers the
// "empty chained reference" branch in resolveChainedRef. A file whose
// body is just "@" (optionally followed by whitespace) would otherwise
// recurse into readRef("") and either silently succeed or produce a
// confusing error. The parser rejects it explicitly at the chain
// boundary.
func TestResolveChainedRef_EmptyChainedRefRejected(t *testing.T) {
	dir := t.TempDir()
	// First ref points at a file whose body is only "@" + whitespace -
	// a malformed chained reference.
	emptyRef := filepath.Join(dir, "empty_ref.txt")
	if err := os.WriteFile(emptyRef, []byte("@   \n\t "), 0o600); err != nil {
		t.Fatalf("write empty_ref: %v", err)
	}
	wfPath := filepath.Join(dir, "wf.yaml")
	wfYAML := "name: x\nversion: 1\nsteps:\n  - id: s\n    instructions: \"@empty_ref.txt\"\n"
	if err := os.WriteFile(wfPath, []byte(wfYAML), 0o600); err != nil {
		t.Fatalf("write wf: %v", err)
	}
	_, err := LoadWorkflow(wfPath)
	if err == nil {
		t.Fatal("LoadWorkflow accepted empty chained reference; want rejection")
	}
	if !strings.Contains(err.Error(), "empty chained reference") {
		t.Errorf("err=%v; want 'empty chained reference' substring", err)
	}
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
