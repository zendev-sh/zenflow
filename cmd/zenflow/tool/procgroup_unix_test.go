//go:build !windows

package tool

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
)

// TestProcGroupKill_Unix verifies that setPlatformProcessGroup
// configures Setpgid:true and installs a non-nil Cancel closure that
// kills the process group via syscall.Kill(-pgid, SIGKILL).
func TestProcGroupKill_Unix(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/echo", "ok")
	setPlatformProcessGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr is nil; setPlatformProcessGroup didn't set it")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid = false; want true so SIGKILL -pgid takes the whole group down")
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel is nil; expected SIGKILL closure")
	}
}

// TestProcGroupKill_NilProcess verifies the nil-guard added
// to the Unix Cancel closure (mirroring the Windows variant). Without
// the guard, calling Cancel on a never-started cmd would panic
// dereferencing cmd.Process.Pid. The Go stdlib guarantees Cancel only
// fires after Start succeeds in production; the guard is defensive
// against direct test invocations and keeps the two platform impls
// symmetric.
func TestProcGroupKill_NilProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/echo", "ok")
	setPlatformProcessGroup(cmd)
	if cmd.Process != nil {
		t.Skip("test precondition: Process should be nil before Start")
	}
	if err := cmd.Cancel(); err != nil {
		t.Errorf("Cancel on never-started cmd returned err: %v (nil-guard regression?)", err)
	}
}

// Compile-time sanity: SysProcAttr.Setpgid is a bool on every POSIX
// build Go supports. Drift on this stdlib type would break the build
// at this declaration before the runtime tests run.
var _ bool = (&syscall.SysProcAttr{}).Setpgid
