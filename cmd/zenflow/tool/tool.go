// Package tool provides built-in tools as []goai.Tool values.
package tool

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zendev-sh/goai"
)

// DefaultTools returns all built-in tools without filesystem containment.
// Use DefaultToolsIn to confine read/write/bash to a workdir.
func DefaultTools() []goai.Tool {
	return DefaultToolsIn("")
}

// DefaultToolsIn returns the built-in tool set with optional workdir
// containment. When workdir is non-empty (must be absolute), read and
// write reject paths that resolve outside it, and bash always runs with
// cmd.Dir = workdir regardless of any LLM-supplied working_directory.
// When workdir is empty, no containment is applied - equivalent to the
// pre-containment behavior. Callers wanting a sandbox should pass an
// absolute path.
// Note: bash containment is best-effort. The shell can still escape via
// `cd ..` mid-command, absolute pipes, or output redirection. True
// isolation requires an OS-level mechanism (bwrap/firejail/chroot) and
// is out of scope for this tool layer.
func DefaultToolsIn(workdir string) []goai.Tool {
	return []goai.Tool{
		bashToolIn(workdir),
		readToolIn(workdir),
		writeToolIn(workdir),
		globToolIn(workdir),
		grepToolIn(workdir),
	}
}

// resolveUnderWorkdir validates that a user-supplied path resolves
// inside workdir. Returns the cleaned absolute path on success.
// Behavior:
// - workdir == "": no check, returns filepath.Clean(p) (legacy mode).
// - p is absolute: must equal workdir or be a descendant.
// - p is relative: joined onto workdir, then must be a descendant.
// - any path containing a traversal that escapes workdir is rejected.
func resolveUnderWorkdir(p, workdir string) (string, error) {
	cleaned := filepath.Clean(p)
	if workdir == "" {
		return cleaned, nil
	}
	var abs string
	if filepath.IsAbs(cleaned) {
		abs = cleaned
	} else {
		abs = filepath.Join(workdir, cleaned)
	}
	rel, err := filepath.Rel(workdir, abs)
	if err != nil {
		return "", fmt.Errorf("path %q is outside workdir %q: %w", p, workdir, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside workdir %q (rel=%s)", p, workdir, rel)
	}
	return abs, nil
}

// setProcessGroup configures cmd to run in its own process group and kill
// the entire tree (including children) when the context cancels. WaitDelay
// ensures cmd.Wait doesn't block forever on pipe drain - after the process
// exits, Go waits up to WaitDelay for pipe goroutines before closing pipes
// forcefully (which unblocks any orphaned child holding them open).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
	setPlatformProcessGroup(cmd)
}
