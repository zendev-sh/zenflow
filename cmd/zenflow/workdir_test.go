package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyWorkdir_Empty verifies applyWorkdir is a no-op when workdir is "".
func TestApplyWorkdir_Empty(t *testing.T) {
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	if err := applyWorkdir(""); err != nil {
		t.Fatalf("applyWorkdir(\"\") = %v, want nil", err)
	}
	cwd, _ := os.Getwd()
	if cwd != origCwd {
		t.Errorf("cwd changed unexpectedly: %q → %q", origCwd, cwd)
	}
}

// TestApplyWorkdir_NonExistent verifies applyWorkdir errors on missing dir.
func TestApplyWorkdir_NonExistent(t *testing.T) {
	origCwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	err := applyWorkdir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("applyWorkdir on missing dir returned nil, want error")
	}
	if !strings.Contains(err.Error(), "--workdir") {
		t.Errorf("error = %q, want to mention --workdir", err)
	}
}

// TestApplyWorkdir_NotADirectory verifies applyWorkdir errors when path is a file.
func TestApplyWorkdir_NotADirectory(t *testing.T) {
	origCwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := applyWorkdir(filePath)
	if err == nil {
		t.Fatal("applyWorkdir on file returned nil, want error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %q, want to mention 'not a directory'", err)
	}
}

// TestApplyWorkdir_InsideZenflowSource verifies the guardrail refuses to run
// when workdir is inside a zenflow checkout. This is the exact pollution
// incident that motivated --workdir - the guardrail prevents re-occurrence.
func TestApplyWorkdir_InsideZenflowSource(t *testing.T) {
	origCwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	tmp := t.TempDir()
	// Plant a fake zenflow go.mod at the tmpdir root.
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module github.com/zendev-sh/zenflow\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// A subdirectory inside the fake zenflow tree.
	sub := filepath.Join(tmp, "cmd", "api")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := applyWorkdir(sub)
	if err == nil {
		t.Fatal("applyWorkdir inside zenflow tree returned nil, want refusal")
	}
	if !strings.Contains(err.Error(), "inside the zenflow source tree") {
		t.Errorf("error = %q, want 'inside the zenflow source tree'", err)
	}
}

// TestApplyWorkdir_CleanDir verifies applyWorkdir chdirs to a clean tmp dir.
// Cleanup ordering matters here: t.Cleanup runs callbacks in LIFO order,
// and t.TempDir registers its `RemoveAll(tmp)` cleanup the first time it
// is called. On Windows, RemoveAll cannot delete a directory that is the
// current working directory of the test process - the open handle on cwd
// makes the unlink fail with "process cannot access the file because it
// is being used by another process". So we MUST call t.TempDir BEFORE
// registering the chdir-back cleanup; that way the LIFO order is:
// 1. our cleanup runs first → chdir back to origCwd
// 2. t.TempDir's cleanup runs second → RemoveAll succeeds
// Reversing the order (chdir cleanup registered first) is the bug that
// surfaced on Windows CI on 2026-05-01.
func TestApplyWorkdir_CleanDir(t *testing.T) {
	origCwd, _ := os.Getwd()
	tmp := t.TempDir() // must precede the chdir cleanup registration
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	if err := applyWorkdir(tmp); err != nil {
		t.Fatalf("applyWorkdir(%q) = %v, want nil", tmp, err)
	}
	cwd, _ := os.Getwd()
	// t.TempDir may resolve through /private/var on macOS - compare via EvalSymlinks.
	cwdResolved, _ := filepath.EvalSymlinks(cwd)
	tmpResolved, _ := filepath.EvalSymlinks(tmp)
	if cwdResolved != tmpResolved {
		t.Errorf("cwd = %q, want %q", cwdResolved, tmpResolved)
	}
}

// TestFindZenflowModuleRoot_WalkUp verifies the walker finds go.mod at an
// ancestor, not just the starting dir.
func TestFindZenflowModuleRoot_WalkUp(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module github.com/zendev-sh/zenflow\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	deep := filepath.Join(tmp, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := findZenflowModuleRoot(deep)
	// Resolve both sides for macOS symlink quirks.
	gotResolved, _ := filepath.EvalSymlinks(got)
	tmpResolved, _ := filepath.EvalSymlinks(tmp)
	if gotResolved != tmpResolved {
		t.Errorf("findZenflowModuleRoot(%q) = %q, want %q", deep, gotResolved, tmpResolved)
	}
}

// TestFindZenflowModuleRoot_NotZenflow verifies the walker ignores go.mod
// files with a different module declaration.
func TestFindZenflowModuleRoot_NotZenflow(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/other\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := findZenflowModuleRoot(tmp); got != "" {
		t.Errorf("findZenflowModuleRoot(%q) = %q, want empty", tmp, got)
	}
}

// TestParseFlags_Workdir verifies parseFlags recognizes --workdir.
func TestParseFlags_Workdir(t *testing.T) {
	f, err := parseFlags([]string{"--workdir", "/tmp/scratch"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if f.workdir != "/tmp/scratch" {
		t.Errorf("workdir = %q, want %q", f.workdir, "/tmp/scratch")
	}
}

// TestParseFlags_WorkdirMissingValue verifies the missing-value error path.
func TestParseFlags_WorkdirMissingValue(t *testing.T) {
	_, err := parseFlags([]string{"--workdir"})
	if err == nil {
		t.Fatal("parseFlags(--workdir) returned nil error, want missing-value error")
	}
	if !strings.Contains(err.Error(), "--workdir requires a value") {
		t.Errorf("error = %q, want 'requires a value'", err)
	}
}
