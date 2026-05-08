package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"time"

	"github.com/zendev-sh/goai"
)

const (
	// bashTimeout is the per-invocation timeout for bash commands.
	bashTimeout = 30 * time.Second
	// bashMaxOutput is the maximum output size (1 MB).
	bashMaxOutput = 1 << 20
)

func bashTool() goai.Tool { return bashToolIn("") }

func bashToolIn(workdir string) goai.Tool {
	return goai.Tool{
		Name:        "bash",
		Description: "Execute a shell command and return its combined stdout and stderr. WARNING: bash is unsandboxed - the command runs with the same privileges as the host process. When a workdir is configured, the LLM-supplied working_directory is ignored and the command is forced to run in the workdir; this does NOT prevent the shell itself from `cd ..`-ing or writing to absolute paths during the command.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute"},"timeout_seconds":{"type":"integer","description":"Per-command timeout in seconds (default 30, max 300)"},"working_directory":{"type":"string","description":"Working directory for the command (ignored when a workdir sandbox is configured)"}},"required":["command"]}`),
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Command          string `json:"command"`
				TimeoutSeconds   int    `json:"timeout_seconds"`
				WorkingDirectory string `json:"working_directory"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}

 // Apply per-invocation timeout (capped at 300s).
 // (2026-05-04) - cap BEFORE the multiplication.
 // `time.Duration(p.TimeoutSeconds) * time.Second` overflows
 // the int64 nanosecond counter for `p.TimeoutSeconds >
 // 9_223_372_036` (about 292 years), wrapping to a large
 // negative value that the post-multiply `> 300s` guard
 // silently failed to catch. The result was a context with
 // an already-expired deadline - every bash call returned
 // `context.DeadlineExceeded`. Since `p.TimeoutSeconds` is
 // LLM-controlled tool input, an adversarial or
 // hallucinating model could trip this with a single large
 // integer.
			timeout := bashTimeout
			if p.TimeoutSeconds > 0 {
				secs := p.TimeoutSeconds
				if secs > 300 {
					secs = 300
				}
				timeout = time.Duration(secs) * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

 // / - pick `sh -c` on Unix and
 // `powershell.exe -Command` on Windows. selectShell hides
 // the platform check; wrapCommand strips stray whitespace
 // for PowerShell. See platform.go for rationale.
			shell, flag := selectShell()
			cmd := exec.CommandContext(ctx, shell, flag, wrapCommand(p.Command))
			if workdir != "" {
 // Sandbox mode: force cmd.Dir to workdir, ignore LLM input.
 // normalize workdir to native separators so the
 // shell receives a path it actually understands.
				cmd.Dir = normalizePath(workdir)
			} else if p.WorkingDirectory != "" {
				cmd.Dir = normalizePath(p.WorkingDirectory)
			}

			setProcessGroup(cmd)

 // Capture stdout and stderr with size limits.
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &limitedWriter{w: &stdout, remaining: bashMaxOutput}
			cmd.Stderr = &limitedWriter{w: &stderr, remaining: bashMaxOutput}

			err := cmd.Run()

 // Combine output, capping total at bashMaxOutput.
			combined := stdout.String() + stderr.String()
			if len(combined) > bashMaxOutput {
				combined = combined[:bashMaxOutput] + "\n...[output truncated at 1MB]"
			}

			if err != nil {
 // Return the output along with the error message so the caller
 // can see stderr even when the command fails.
				return combined + err.Error(), nil
			}
			return combined, nil
		},
	}
}

// limitedWriter wraps a writer and stops writing after a byte limit.
type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		return len(p), nil // discard silently
	}
	if len(p) > lw.remaining {
		p = p[:lw.remaining]
	}
	n, err := lw.w.Write(p)
	lw.remaining -= n
	return n, err
}
