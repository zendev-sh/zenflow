//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
)

func setPlatformProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// - match the Windows variant's defensive nil guard.
		// In production cmd.Process is set by exec.Cmd before Cancel
		// can fire (Go stdlib guarantee), but the explicit check
		// keeps the two platform implementations symmetric and
		// future-proof against any direct-from-test invocations.
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
