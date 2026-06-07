package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zendev-sh/goai"
)

func writeTool() goai.Tool { return writeToolIn("") }

func writeToolIn(workdir string) goai.Tool {
	return goai.NewTool("write",
		"Write content to a file at the given path. Creates parent directories as needed. Rejects paths containing '..' traversal or that resolve outside the configured workdir.",
		func(_ context.Context, p struct {
			Path    string `json:"path" jsonschema:"description=The file path to write"`
			Content string `json:"content" jsonschema:"description=The content to write"`
		}) (string, error) {
			// Reject path traversal: check raw path for ".." components before cleaning.
			for part := range strings.SplitSeq(filepath.ToSlash(p.Path), "/") {
				if part == ".." {
					return "", fmt.Errorf("path %q contains '..' traversal", p.Path)
				}
			}
			// normalize separators so Windows callers using
			// forward slashes (LLMs love `/`) end up at the same path
			// as callers using `\`.
			cleaned, err := resolveUnderWorkdir(normalizePath(p.Path), workdir)
			if err != nil {
				return "", err
			}

			// Create parent directories.
			dir := filepath.Dir(cleaned)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return "", err
			}

			if err := os.WriteFile(cleaned, []byte(p.Content), 0600); err != nil {
				return "", err
			}
			return "ok", nil
		})
}
