package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepToolIn_PathInsideWorkdir verifies that a relative path resolves
// correctly under workdir and grep finds matches there.
func TestGrepToolIn_PathInsideWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("hello workdir\nother line"), 0644); err != nil {
		t.Fatal(err)
	}

	g := grepToolIn(dir)
	args, _ := json.Marshal(map[string]any{
		"pattern": "hello",
		"path":    "target.txt",
	})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello workdir") {
		t.Errorf("expected 'hello workdir' in result, got %q", result)
	}
}

// TestGrepToolIn_AbsolutePathOutsideWorkdir_Rejected verifies that an absolute
// path pointing outside the workdir is rejected.
func TestGrepToolIn_AbsolutePathOutsideWorkdir_Rejected(t *testing.T) {
	dir := t.TempDir()
	g := grepToolIn(dir)
	args, _ := json.Marshal(map[string]any{
		"pattern": "root",
		"path":    "/etc/passwd",
	})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected containment error for '/etc/passwd', got nil")
	}
	if !strings.Contains(err.Error(), "outside workdir") {
		t.Errorf("expected 'outside workdir' in error, got: %v", err)
	}
}

// TestGrepToolIn_DotDotPathRejected verifies that a path starting with ".."
// is rejected when a workdir is configured.
func TestGrepToolIn_DotDotPathRejected(t *testing.T) {
	dir := t.TempDir()
	g := grepToolIn(dir)
	args, _ := json.Marshal(map[string]any{
		"pattern": "anything",
		"path":    "../escape",
	})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected containment error for '../escape', got nil")
	}
	if !strings.Contains(err.Error(), "outside workdir") {
		t.Errorf("expected 'outside workdir' in error, got: %v", err)
	}
}

// TestGrepToolIn_EmptyPathDefaultsToWorkdir verifies that an empty path
// searches the entire workdir.
func TestGrepToolIn_EmptyPathDefaultsToWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme here"), 0644); err != nil {
		t.Fatal(err)
	}

	g := grepToolIn(dir)
	args, _ := json.Marshal(map[string]any{
		"pattern": "findme",
		"path":    "",
	})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "findme here") {
		t.Errorf("expected 'findme here' in result, got %q", result)
	}
}

// TestGrepToolIn_NoWorkdir_AllowsAbsolutePath verifies legacy mode (no workdir)
// passes arbitrary paths through without containment checks.
func TestGrepToolIn_NoWorkdir_AllowsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nosandbox content"), 0644); err != nil {
		t.Fatal(err)
	}

	g := grepToolIn("") // no workdir
	args, _ := json.Marshal(map[string]any{
		"pattern": "nosandbox",
		"path":    dir,
	})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "nosandbox content") {
		t.Errorf("expected 'nosandbox content' in result, got %q", result)
	}
}

// TestGrepToolIn_InvalidJSON verifies JSON decode error is returned.
func TestGrepToolIn_InvalidJSON(t *testing.T) {
	g := grepToolIn(t.TempDir())
	_, err := g.Execute(context.Background(), json.RawMessage(`not-json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestGrepToolIn_MissingPath verifies that a non-existent search path returns
// an error (mirrors Unix grep exit code 2 + "No such file or directory").
// This test runs on all platforms because the pre-check is a Go-level os.Stat
// in grep.go, not delegated to the underlying grep or PowerShell.
func TestGrepToolIn_MissingPath(t *testing.T) {
	g := grepToolIn("") // no workdir - direct path
	args, _ := json.Marshal(map[string]any{
		"pattern": "anything",
		"path":    "/nonexistent/path/that/does/not/exist",
	})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !strings.Contains(err.Error(), "grep:") {
		t.Errorf("expected 'grep:' prefix in error message, got: %v", err)
	}
}

// TestDefaultToolsIn_GrepIsContained verifies that DefaultToolsIn wires
// grepToolIn (not the bare grepTool), so the grep returned enforces workdir.
func TestDefaultToolsIn_GrepIsContained(t *testing.T) {
	dir := t.TempDir()
	tools := DefaultToolsIn(dir)

	var execFn func(context.Context, json.RawMessage) (string, error)
	for i := range tools {
		if tools[i].Name == "grep" {
			execFn = tools[i].Execute
			break
		}
	}
	if execFn == nil {
		t.Fatal("grep tool not found in DefaultToolsIn result")
	}

	// Absolute path outside workdir must be rejected.
	args, _ := json.Marshal(map[string]any{
		"pattern": "root",
		"path":    "/etc/passwd",
	})
	_, err := execFn(context.Background(), args)
	if err == nil {
		t.Fatal("expected containment error for '/etc/passwd' from DefaultToolsIn grep")
	}
}
