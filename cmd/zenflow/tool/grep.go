package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/zendev-sh/goai"
)

const grepTimeout = 30 * time.Second
const grepMaxOutput = 1 << 20 // 1 MB

// grepToolIn returns a grep tool optionally confined to workdir. When workdir
// is non-empty, p.Path is rooted and validated under workdir before being
// passed to the subprocess; patterns that escape the workdir are rejected.
// The underlying search mechanism is platform-specific: Unix uses grep(1);
// Windows uses PowerShell Select-String. Both produce equivalent output
// (file:line:content lines) and honour the same input JSON schema.
func grepToolIn(workdir string) goai.Tool {
	return goai.Tool{
		Name:        "grep",
		Description: "Search for a pattern in files. Returns matching lines with file names and line numbers. Uses fixed-string matching by default; set regex=true for regex patterns. When a workdir is configured, the search path must be within the workdir.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"The pattern to search for"},"path":{"type":"string","description":"The file or directory to search in (relative to workdir when a workdir is configured)"},"regex":{"type":"boolean","description":"Use regex matching instead of fixed-string (default: false)"}},"required":["pattern","path"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Regex   bool   `json:"regex"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}

			// Resolve and validate the search path against workdir.
			searchPath := p.Path
			if workdir != "" {
				if searchPath == "" {
					searchPath = workdir
				} else {
					resolved, err := resolveUnderWorkdir(normalizePath(searchPath), workdir)
					if err != nil {
						return "", err
					}
					searchPath = resolved
				}
			}

			// Pre-check path existence: mirrors Unix grep exit code 2 +
			// "No such file or directory" stderr message. Without this,
			// Windows PowerShell would silently return empty output for a
			// missing path, making it indistinguishable from "no matches".
			if _, statErr := os.Stat(searchPath); statErr != nil {
				return "", fmt.Errorf("grep: %s: %w", searchPath, statErr)
			}

			// Apply per-invocation timeout.
			grepCtx, cancel := context.WithTimeout(ctx, grepTimeout)
			defer cancel()

			cmd := buildGrepCmd(grepCtx, p.Pattern, searchPath, p.Regex)
			setProcessGroup(cmd)

			// Cap output size.
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &limitedWriter{w: &stdout, remaining: grepMaxOutput}
			cmd.Stderr = &limitedWriter{w: &stderr, remaining: grepMaxOutput}

			err := cmd.Run()
			combined := stdout.String() + stderr.String()
			if len(combined) > grepMaxOutput {
				combined = combined[:grepMaxOutput]
			}

			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
					return "", nil // no matches
				}
				return combined, nil
			}
			return combined, nil
		},
	}
}
