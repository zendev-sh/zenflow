package main

// workdir.go - --workdir handling. The CLI sandbox exists so
// LLM-invoked tools (write, bash, ...) cannot pollute the caller's
// source tree. When unset, the CLI runs in the caller's current
// directory; when set, applyWorkdir validates the path, refuses to
// run inside a zenflow checkout, then chdirs into it.
// The "is this a zenflow checkout?" check walks up from the workdir
// looking for a go.mod whose module declaration matches
// `github.com/zendev-sh/zenflow`; this catches the "accidentally
// pointed --workdir at the repo root" footgun that motivated the
// flag in the first place.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// filepathAbs is a test seam for applyWorkdir's filepath.Abs call.
// Injectable so tests can exercise the "Abs error" branch without needing
// to corrupt the host's cwd state.
var filepathAbs = filepath.Abs

func applyWorkdir(workdir string) error {
	if workdir == "" {
		return nil
	}
	abs, err := filepathAbs(workdir)
	if err != nil {
		return fmt.Errorf("--workdir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("--workdir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--workdir %q is not a directory", abs)
	}
	// Guardrail: refuse to run if workdir is inside a zenflow checkout. We
	// detect this by walking up looking for a go.mod whose module declaration
	// matches github.com/zendev-sh/zenflow. This is cheap (a few stats) and
	// catches the "accidentally ran from repo root" footgun.
	if modPath := findZenflowModuleRoot(abs); modPath != "" {
		return fmt.Errorf("--workdir %q is inside the zenflow source tree (%s); refusing to run - choose a scratch directory (e.g., mktemp -d)", abs, modPath)
	}
	if err := os.Chdir(abs); err != nil {
		return fmt.Errorf("--workdir: chdir: %w", err)
	}
	return nil
}

// findZenflowModuleRoot walks up from dir looking for a go.mod that declares
// module github.com/zendev-sh/zenflow. Returns the directory containing such
// a go.mod, or "" if none found. Stops at filesystem root.
func findZenflowModuleRoot(dir string) string {
	const zenflowModuleLine = "module github.com/zendev-sh/zenflow"
	for {
		gomod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(gomod); err == nil {
 // Match either exact line or line+newline prefix (handles trailing /)
 // by checking that the module line appears as a full line.
			for line := range strings.Lines(string(data)) {
				trimmed := strings.TrimSpace(line)
				if trimmed == zenflowModuleLine || strings.HasPrefix(trimmed, zenflowModuleLine+"/") {
					return dir
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
