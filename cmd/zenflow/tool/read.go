package tool

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/zendev-sh/goai"
)

const readMaxSize = 1 << 20 // 1 MB

// readAll wraps io.ReadAll. Injectable for testing error paths.
var readAll = io.ReadAll

func readTool() goai.Tool { return readToolIn("") }

func readToolIn(workdir string) goai.Tool {
	return goai.NewTool("read",
		"Read the contents of a file at the given path (capped at 1 MB). Rejects paths that resolve outside the configured workdir.",
		func(_ context.Context, p struct {
			Path string `json:"path" jsonschema:"description=The file path to read"`
		}) (string, error) {
			// normalize path separators so a Windows caller
			// passing `subdir/file.txt` works the same as
			// `subdir\file.txt`. Containment + workdir join already
			// run inside resolveUnderWorkdir; normalizePath here
			// canonicalises before that.
			resolved, err := resolveUnderWorkdir(normalizePath(p.Path), workdir)
			if err != nil {
				return "", err
			}
			// read returns the raw bytes verbatim, so CRLF
			// content authored on Windows survives a round-trip
			// untouched. We deliberately do NOT translate CRLF↔LF -
			// callers (LLMs editing source files) need to see what is
			// actually on disk.
			f, err := os.Open(resolved)
			if err != nil {
				return "", err
			}
			defer func() { _ = f.Close() }()
			data, err := readAll(io.LimitReader(f, readMaxSize+1))
			if err != nil {
				return "", err
			}
			if len(data) > readMaxSize {
				return string(data[:readMaxSize]), fmt.Errorf("file exceeds 1 MB limit (truncated)")
			}
			return string(data), nil
		})
}
