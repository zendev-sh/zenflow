//go:build windows

package tool

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Windows constants from <winnt.h>. Go's syscall package exposes some
// of these but not all consistently across versions, so we declare them
// locally and rely on the kernel32 ABI staying stable (which it has,
// since Windows NT 4.0).
const (
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	createNoWindow        = 0x08000000 // CREATE_NO_WINDOW
)

// taskkillPath is the absolute path to taskkill.exe, resolved once at
// package init. Using the absolute path prevents PATH-poisoning.
var taskkillPath = func() string {
	// Prefer the well-known system32 location. Fall back to LookPath if
	// the env var is missing (unusual but safe).
	if root := os.Getenv("SystemRoot"); root != "" {
		return root + `\System32\taskkill.exe`
	}
	if p, err := exec.LookPath("taskkill"); err == nil {
		return p
	}
	return "taskkill"
}()

// setPlatformProcessGroup configures cmd to spawn into its own process
// group, hidden from any console window the parent might own, and
// arranges for cancel to nuke the entire group with `taskkill /T /F`
// when the context expires.
// Why CREATE_NEW_PROCESS_GROUP: Windows has no fork/setpgid; the
// closest thing is a "process group" managed by the kernel. We need our
// own group so that cancel killing the group doesn't also kill the
// CLI's parent shell. CREATE_NEW_PROCESS_GROUP also disables Ctrl+C
// propagation from the parent console (the CLI side handles signal
// translation separately; here we just make sure spawned children are
// insulated).
// Why CREATE_NO_WINDOW: when the CLI runs from a non-console parent
// (an editor's integrated terminal, a launcher daemon, a Windows
// service, etc.) any spawned command otherwise pops a flickering
// console window. CREATE_NO_WINDOW suppresses it. The downside is the
// child cannot read from a real console - fine for our use case (we
// always pipe stdin/stdout via os/exec).
// Cancel impl: Windows does not have a "kill process group" syscall.
// `taskkill /T /F /PID <pid>` walks the parent-pid tree and terminates
// every descendant, which is the closest equivalent. We exec it via
// exec.Command rather than calling TerminateJobObject because the
// latter requires job-object setup at spawn time (an order of
// magnitude more code), and taskkill is shipped with every supported
// Windows release since Windows XP.
// Build-tag-only: this file compiles only on Windows, so the Cancel
// closure can call out to taskkill without affecting the macOS build.
// Tests for this branch live in procgroup_windows_test.go (also build-
// tag-gated). Cross-platform tests using the `goos` indirection cover
// the rest of the platform-aware code.
func setPlatformProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
 // Best effort; ignore taskkill's exit code. If the process
 // already exited cleanly we still return nil so the caller
 // doesn't see a spurious error.
		_ = exec.Command(taskkillPath, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return nil
	}
}
