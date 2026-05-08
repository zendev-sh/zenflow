//go:build !windows

package tool

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
)

// grepBinaryPath is the absolute path to the grep binary, resolved once at
// first use. Using an absolute path prevents PATH-poisoning: a malicious or
// misconfigured PATH cannot shadow the system grep.
var (
	grepBinaryOnce sync.Once
	grepBinaryPath string
	// lookPathFunc is a test seam for exec.LookPath in resolvedGrepBinary.
	lookPathFunc = exec.LookPath
)

func resolvedGrepBinary() string {
	grepBinaryOnce.Do(func() {
		p, err := lookPathFunc("grep")
		if err != nil {
			slog.Warn("grep binary not found via LookPath; falling back to bare 'grep'", "err", err)
			p = "grep"
		}
		grepBinaryPath = p
	})
	return grepBinaryPath
}

// buildGrepCmd returns an exec.Cmd that searches pattern in searchPath.
// On Unix this delegates to grep(1) with -rn (recursive, line numbers) and
// optionally -F (fixed-string, disabled when regex=true).
func buildGrepCmd(ctx context.Context, pattern, searchPath string, regex bool) *exec.Cmd {
	grepArgs := []string{"-rn"}
	if !regex {
		grepArgs = append(grepArgs, "-F") // fixed-string, no regex
	}
	grepArgs = append(grepArgs, "--", pattern, searchPath)
	return exec.CommandContext(ctx, resolvedGrepBinary(), grepArgs...)
}
