//go:build windows

package tool

import (
	"context"
	"fmt"
	"os/exec"
)

// buildGrepCmd returns an exec.Cmd that searches pattern in searchPath on
// Windows. Since grep(1) is not available on plain Windows installations,
// this implementation delegates to PowerShell's Select-String cmdlet which
// provides equivalent recursive, line-numbered pattern matching.
// The pattern and path are passed via environment variables (ZF_GREP_PATTERN
// and ZF_GREP_PATH) rather than inline script text to avoid shell injection:
// PowerShell reads them with $env:VAR inside the -Command script so no
// quoting or escaping of user-supplied text is required.
// Output format mirrors grep(1): "file:linenum:content" lines, one per match.
// When regex=false (the default), Select-String uses -SimpleMatch for literal
// fixed-string matching, which is equivalent to grep -F.
// Exit code mapping:
// - 0: matches found (grep exit 0)
// - 1: no matches (PowerShell exits 0 with no output; grep exits 1)
// The caller treats empty output as "no matches", same as the Unix path.
// Cross-platform notes:
// - Path separator normalized to '/' (matches Unix grep output).
// - Missing path: pre-checked via os.Stat in grep.go; same error semantics as Unix.
// - Regex dialect: when regex:true, .NET regex (Windows) may interpret
// patterns differently from POSIX BRE/ERE (Unix grep). Keep patterns
// simple or use fixed-string mode for consistency.
// - Binary files: PowerShell may emit garbled lines; Unix grep skips
// them. Future: add a binary-detection skip if it becomes a problem.
func buildGrepCmd(ctx context.Context, pattern, searchPath string, regex bool) *exec.Cmd {
	simpleMatch := ""
	if !regex {
		simpleMatch = " -SimpleMatch"
	}
	// Recurse into subdirectories with -Recurse. Include line numbers with
	// Select-Object to emit "filename:lineNumber:line" triplets compatible
	// with grep -rn output format.
	// Path separators are normalized from '\' to '/' to match Unix grep output
	// so that LLM path parsing works identically across platforms.
	script := fmt.Sprintf(
		"Get-ChildItem -Path $env:ZF_GREP_PATH -Recurse -File -ErrorAction SilentlyContinue |"+
			" Select-String -Pattern $env:ZF_GREP_PATTERN%s -ErrorAction SilentlyContinue |"+
			" ForEach-Object { $path = $_.Path -replace '\\\\', '/'; \"$path`:$($_.LineNumber):$($_.Line)\" }",
		simpleMatch,
	)
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	// Inject pattern and path as env vars to avoid any shell-injection risk.
	cmd.Env = append(cmd.Environ(),
		"ZF_GREP_PATTERN="+pattern,
		"ZF_GREP_PATH="+searchPath,
	)
	return cmd
}
