package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobToolIn_RelativePatternMatchesInsideWorkdir verifies that a
// relative pattern like "*.txt" resolves matches under workdir.
func TestGlobToolIn_RelativePatternMatchesInsideWorkdir(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	g := globToolIn(dir)
	args, _ := json.Marshal(map[string]string{"pattern": "*.txt"})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 2 {
		t.Errorf("got %d matches, want 2: %v", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, ".txt") {
			t.Errorf("unexpected match %q - expected only .txt files", line)
		}
		if !strings.HasPrefix(line, dir) {
			t.Errorf("match %q does not start with workdir %q", line, dir)
		}
	}
}

// TestGlobToolIn_DotDotPatternRejected verifies that a pattern starting with
// ".." is rejected before the Glob call.
func TestGlobToolIn_DotDotPatternRejected(t *testing.T) {
	dir := t.TempDir()
	g := globToolIn(dir)
	args, _ := json.Marshal(map[string]string{"pattern": "../*"})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for '../*' pattern, got nil")
	}
	if !strings.Contains(err.Error(), "escapes the workdir") {
		t.Errorf("expected 'escapes the workdir' in error, got: %v", err)
	}
}

// TestGlobToolIn_AbsolutePatternRejected verifies that an absolute pattern
// like "/etc/*" is rejected when a workdir is configured.
func TestGlobToolIn_AbsolutePatternRejected(t *testing.T) {
	dir := t.TempDir()
	g := globToolIn(dir)
	args, _ := json.Marshal(map[string]string{"pattern": "/etc/*"})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for '/etc/*' pattern, got nil")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("expected 'absolute path' in error, got: %v", err)
	}
}

// TestGlobToolIn_NoWorkdir_AllowsAbsolute verifies legacy mode (no workdir)
// accepts absolute patterns unchanged.
func TestGlobToolIn_NoWorkdir_AllowsAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	g := globToolIn("") // no workdir - unconstrained
	args, _ := json.Marshal(map[string]string{"pattern": filepath.Join(dir, "*.txt")})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "file.txt") {
		t.Errorf("expected 'file.txt' in result, got %q", result)
	}
}

// TestGlobToolIn_InvalidJSON verifies JSON decode error is returned.
func TestGlobToolIn_InvalidJSON(t *testing.T) {
	g := globToolIn(t.TempDir())
	_, err := g.Execute(context.Background(), json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestGlobToolIn_InvalidPattern verifies that a malformed pattern returns an
// error from filepath.Glob.
func TestGlobToolIn_InvalidPattern(t *testing.T) {
	g := globToolIn(t.TempDir())
	args, _ := json.Marshal(map[string]string{"pattern": "[unclosed"})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for malformed glob pattern")
	}
}

// TestGlobToolIn_NoMatches verifies that a pattern with no matches returns
// an empty string (not an error).
func TestGlobToolIn_NoMatches(t *testing.T) {
	g := globToolIn(t.TempDir())
	args, _ := json.Marshal(map[string]string{"pattern": "*.nonexistent"})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}

// TestDefaultToolsIn_GlobIsContained verifies that DefaultToolsIn wires
// globToolIn (not the bare globTool), so the glob returned by DefaultToolsIn
// enforces workdir containment.
func TestDefaultToolsIn_GlobIsContained(t *testing.T) {
	dir := t.TempDir()
	tools := DefaultToolsIn(dir)

	var execFn func(context.Context, json.RawMessage) (string, error)
	for i := range tools {
		if tools[i].Name == "glob" {
			execFn = tools[i].Execute
			break
		}
	}
	if execFn == nil {
		t.Fatal("glob tool not found in DefaultToolsIn result")
	}

	// Absolute pattern must be rejected.
	args, _ := json.Marshal(map[string]string{"pattern": "/etc/*"})
	_, err := execFn(context.Background(), args)
	if err == nil {
		t.Fatal("expected containment error for '/etc/*' from DefaultToolsIn glob")
	}
}
