//go:build windows

package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildGrepCmd_Windows_FixedString verifies that buildGrepCmd on Windows
// produces a PowerShell command with -SimpleMatch when regex=false.
func TestBuildGrepCmd_Windows_FixedString(t *testing.T) {
	cmd := buildGrepCmd(context.Background(), "testpattern", "C:\\testpath", false)
	if cmd == nil {
		t.Fatal("buildGrepCmd returned nil")
	}
	if len(cmd.Args) < 1 || !strings.HasSuffix(cmd.Args[0], "powershell.exe") {
		t.Errorf("expected powershell.exe, got args[0]=%q", cmd.Args[0])
	}
	// The script (last arg) must contain -SimpleMatch for fixed-string mode.
	script := strings.Join(cmd.Args, " ")
	if !strings.Contains(script, "-SimpleMatch") {
		t.Errorf("expected -SimpleMatch in fixed-string mode, script=%q", script)
	}
}

// TestBuildGrepCmd_Windows_Regex verifies that buildGrepCmd on Windows
// omits -SimpleMatch when regex=true.
func TestBuildGrepCmd_Windows_Regex(t *testing.T) {
	cmd := buildGrepCmd(context.Background(), "test.*pattern", "C:\\testpath", true)
	if cmd == nil {
		t.Fatal("buildGrepCmd returned nil")
	}
	script := strings.Join(cmd.Args, " ")
	if strings.Contains(script, "-SimpleMatch") {
		t.Errorf("expected no -SimpleMatch in regex mode, script=%q", script)
	}
}

// TestBuildGrepCmd_Windows_EnvVars verifies pattern and path are passed via
// environment variables, not inline in the script (injection-safe).
func TestBuildGrepCmd_Windows_EnvVars(t *testing.T) {
	cmd := buildGrepCmd(context.Background(), "my pattern", `C:\my path`, false)
	if cmd == nil {
		t.Fatal("buildGrepCmd returned nil")
	}
	// ZF_GREP_PATTERN and ZF_GREP_PATH must appear in Env, not Args.
	var foundPattern, foundPath bool
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "ZF_GREP_PATTERN=") {
			foundPattern = true
		}
		if strings.HasPrefix(kv, "ZF_GREP_PATH=") {
			foundPath = true
		}
	}
	if !foundPattern {
		t.Error("ZF_GREP_PATTERN not found in cmd.Env")
	}
	if !foundPath {
		t.Error("ZF_GREP_PATH not found in cmd.Env")
	}
}

// TestGrepToolIn_Windows_FindsMatch is a live integration test (requires
// PowerShell on the host). Skipped when not on Windows.
func TestGrepToolIn_Windows_FindsMatch(t *testing.T) {
	// This test only runs on Windows (enforced by build tag).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "win.txt"), []byte("windows grep works\nother line"), 0644); err != nil {
		t.Fatal(err)
	}

	g := grepToolIn(dir)
	args, _ := json.Marshal(map[string]any{
		"pattern": "windows grep works",
		"path":    "win.txt",
	})
	result, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "windows grep works") {
		t.Errorf("expected 'windows grep works' in result, got %q", result)
	}
}
