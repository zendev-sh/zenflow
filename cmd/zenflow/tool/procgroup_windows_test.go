//go:build windows

package tool

// procgroup_windows_test.go -  +  evidence.
// These assertions can only run on Windows: syscall.SysProcAttr's
// CreationFlags field is Windows-only, and exec.Cmd.Cancel calling
// taskkill needs the actual Windows kernel to do the right thing.
// On macOS development boxes the file is excluded by the //go:build
// windows tag so it has zero impact on host coverage. Real verification
// happens in Windows CI (post-merge).

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
)

// TestProcGroupKill_Windows asserts that setPlatformProcessGroup
// configures CreationFlags with both CREATE_NEW_PROCESS_GROUP and
// CREATE_NO_WINDOW, and installs a non-nil Cancel.
// Cmd is built with exec.CommandContext because Go ≥1.20 enforces that
// any *exec.Cmd whose Cancel field is set must have been created with a
// context - otherwise Start returns "command with a non-nil Cancel was
// not created with CommandContext". setPlatformProcessGroup wires Cancel
// for taskkill, so production callers always go through CommandContext;
// the test mirrors that contract here so the assertion runs on the same
// shape as production.
func TestProcGroupKill_Windows(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd.exe", "/c", "rem")
	setPlatformProcessGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil; setPlatformProcessGroup didn't set it")
	}
	flags := cmd.SysProcAttr.CreationFlags
	if flags&createNewProcessGroup == 0 {
		t.Errorf("CreationFlags = %#x missing CREATE_NEW_PROCESS_GROUP (%#x)", flags, createNewProcessGroup)
	}
	if flags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x missing CREATE_NO_WINDOW (%#x)", flags, createNoWindow)
	}
	if cmd.Cancel == nil {
		t.Error("Cancel is nil; expected taskkill closure")
	}
}

// TestProcGroupKill_TaskkillFires runs a real Windows process and
// verifies that invoking the Cancel closure does not error out - even
// when the process has already exited (taskkill returns non-zero in
// that case, which our closure swallows). The actual kill semantics
// are exercised by the integration suite (PTY tests) on
// Windows CI.
func TestProcGroupKill_TaskkillFires(t *testing.T) {
	// CommandContext (not Command) - see the rationale on
	// TestProcGroupKill_Windows: setPlatformProcessGroup sets cmd.Cancel,
	// which Start will reject unless the cmd was created with a
	// context. Production builds the cmd via exec.CommandContext in
	// bash.go; mirror that here so we test the same code path.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "rem")
	setPlatformProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Wait()
	// After the process exits, Cancel should still be safe to call.
	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel after Wait returned err: %v", err)
	}
}

// TestProcGroupKill_NilProcess asserts the Cancel closure tolerates a
// never-started cmd (Process is nil). We rely on this so the WaitDelay
// path in setProcessGroup doesn't panic when the underlying exec fails
// before Start completes.
func TestProcGroupKill_NilProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd.exe", "/c", "rem")
	setPlatformProcessGroup(cmd)
	if cmd.Process != nil {
		t.Skip("test precondition violated: Process should be nil before Start")
	}
	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel on never-started cmd returned err: %v", err)
	}
}

// Compile-time sanity: SysProcAttr.CreationFlags should be a uint32 on
// every Windows arch Go supports. If a future Go change renames it,
// this var declaration breaks the build before the runtime tests run.
var _ uint32 = (&syscall.SysProcAttr{}).CreationFlags
