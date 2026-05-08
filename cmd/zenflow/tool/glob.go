package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zendev-sh/goai"
)

func globTool() goai.Tool { return globToolIn("") }

// globToolIn returns a glob tool confined to workdir. When workdir is
// non-empty, absolute patterns and patterns starting with ".." are rejected
// immediately, and the pattern is rooted under workdir. Each match is then
// validated through resolveUnderWorkdir so symlinks that point outside workdir
// are also filtered out.
func globToolIn(workdir string) goai.Tool {
	return goai.Tool{
		Name:        "glob",
		Description: "Search for files matching a glob pattern. When a workdir is configured, the pattern is relative to the workdir and cannot escape it.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"The glob pattern to match (relative to workdir when a workdir is configured)"}},"required":["pattern"]}`),
		Execute: func(_ context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Pattern string `json:"pattern"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}

			if workdir == "" {
 // Legacy / unconstrained mode.
				matches, err := filepath.Glob(p.Pattern)
				if err != nil {
					return "", err
				}
				return strings.Join(matches, "\n"), nil
			}

 // Workdir-contained mode.
 // Reject patterns that are absolute or start with "..".
			cleaned := filepath.Clean(p.Pattern)
			if filepath.IsAbs(cleaned) {
				return "", fmt.Errorf("glob pattern %q is an absolute path - use a relative pattern within the workdir", p.Pattern)
			}
			if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("glob pattern %q escapes the workdir", p.Pattern)
			}

 // Root the pattern under workdir.
			rootedPattern := filepath.Join(workdir, p.Pattern)
			matches, err := filepath.Glob(rootedPattern)
			if err != nil {
				return "", err
			}

 // Filter: drop any match that resolves outside workdir (e.g. via symlink).
			safe := matches[:0]
			for _, m := range matches {
				if _, verr := resolveUnderWorkdir(m, workdir); verr == nil {
					safe = append(safe, m)
				}
			}
			return strings.Join(safe, "\n"), nil
		},
	}
}
