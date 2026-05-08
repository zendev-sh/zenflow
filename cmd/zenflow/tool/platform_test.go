package tool

// platform_test.go - evidence (, , ,).
// Tests use the `goos` package variable to simulate Windows on a non-
// Windows host. Each test snapshots and restores `goos` so cases run
// independently regardless of order.

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withGOOS overrides `goos` for the duration of t. Restores on cleanup.
// Centralised so future tests don't each re-implement save/restore.
func withGOOS(t *testing.T, fake string) {
	t.Helper()
	orig := goos
	goos = fake
	t.Cleanup(func() { goos = orig })
}

// =============================================================================
// - Shell detection in CLI bash tool
// =============================================================================

// TestBashTool_ShellDetection asserts selectShell picks the right shell
// per GOOS. Table-driven, covers both branches via the `goos` indirection.
func TestBashTool_ShellDetection(t *testing.T) {
	cases := []struct {
		goos      string
		wantShell string
		wantFlag  string
	}{
		{"linux", "sh", "-c"},
		{"darwin", "sh", "-c"},
		{"freebsd", "sh", "-c"},
		{"windows", "powershell.exe", "-Command"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			withGOOS(t, tc.goos)
			gotShell, gotFlag := selectShell()
			if gotShell != tc.wantShell {
				t.Errorf("shell = %q, want %q", gotShell, tc.wantShell)
			}
			if gotFlag != tc.wantFlag {
				t.Errorf("flag = %q, want %q", gotFlag, tc.wantFlag)
			}
		})
	}
}

// =============================================================================
// - PowerShell syntax fallback
// =============================================================================

// TestBashTool_PowerShellSyntax asserts wrapCommand strips whitespace on
// Windows (so a stray newline doesn't break PowerShell parsing) and is
// a no-op on POSIX.
func TestBashTool_PowerShellSyntax(t *testing.T) {
	cases := []struct {
		name string
		goos string
		in   string
		want string
	}{
		{"posix_passthrough", "linux", "echo hello\n", "echo hello\n"},
		{"posix_preserves_pipes", "darwin", "ls | grep foo && echo ok", "ls | grep foo && echo ok"},
		{"windows_trims_whitespace", "windows", "  Get-Process  \n", "Get-Process"},
		{"windows_preserves_inner_pipes", "windows", "Get-Process | Where Name -eq foo", "Get-Process | Where Name -eq foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			if got := wrapCommand(tc.in); got != tc.want {
				t.Errorf("wrapCommand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBashTool_PowerShell_ExecPathOnWindows asserts that under
// goos=windows, the exec.Cmd built by bashTool uses powershell.exe with
// -Command. We don't actually run it on macOS - exec would fail without
// powershell on PATH - but we can inspect cmd.Path / cmd.Args by
// shimming exec.LookPath via a tiny wrapper.
// Instead of shimming LookPath (intrusive), we directly assert
// selectShell's contract here, treating it as the single source of
// truth for which shell bashTool will spawn.
func TestBashTool_PowerShell_ExecPathOnWindows(t *testing.T) {
	withGOOS(t, "windows")
	shell, flag := selectShell()
	if shell != "powershell.exe" {
		t.Fatalf("shell=%q, want powershell.exe", shell)
	}
	if flag != "-Command" {
		t.Fatalf("flag=%q, want -Command", flag)
	}
	// Verify a constructed cmd matches the shape bashTool will produce.
	cmd := exec.Command(shell, flag, wrapCommand("Get-Date"))
	if len(cmd.Args) != 3 {
		t.Fatalf("len(cmd.Args)=%d, want 3", len(cmd.Args))
	}
	if cmd.Args[0] != "powershell.exe" || cmd.Args[1] != "-Command" || cmd.Args[2] != "Get-Date" {
		t.Errorf("cmd.Args=%v, want [powershell.exe -Command Get-Date]", cmd.Args)
	}
}

// =============================================================================
// - Path normalization (workdir + drive letters)
// =============================================================================

// TestPathNormalize asserts forward-slash paths get cleaned correctly
// on both platforms. On Windows we additionally expect `\` separators.
// Important caveat: filepath.FromSlash is host-dependent - on macOS it
// is a no-op even when goos="windows" - so the windows-flavoured cases
// only assert the Clean part. Real Windows-side separator translation
// is exercised by Windows CI (post-merge follow-up).
func TestPathNormalize(t *testing.T) {
	cases := []struct {
		name string
		goos string
		in   string
		want string
	}{
		{"unix_relative", "linux", "foo/bar/../baz", filepath.Clean("foo/bar/../baz")},
		{"unix_absolute", "linux", "/tmp/foo/./bar", filepath.Clean("/tmp/foo/./bar")},
		{"windows_drive_letter", "windows", "C:\\Users\\foo", filepath.Clean("C:\\Users\\foo")},
		{"windows_forward_slash", "windows", "C:/Users/foo", filepath.Clean(filepath.FromSlash("C:/Users/foo"))},
		{"windows_drive_relative", "windows", "C:foo", filepath.Clean(filepath.FromSlash("C:foo"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			if got := normalizePath(tc.in); got != tc.want {
				t.Errorf("normalizePath(%q) goos=%s → %q, want %q", tc.in, tc.goos, got, tc.want)
			}
		})
	}
}

// =============================================================================
// - Env vars case-insensitive on Windows
// =============================================================================

// TestEnvVar_CaseInsensitive asserts equalEnvKey is case-folded on
// Windows and case-sensitive on POSIX.
func TestEnvVar_CaseInsensitive(t *testing.T) {
	cases := []struct {
		name string
		goos string
		a, b string
		want bool
	}{
		{"unix_exact", "linux", "PATH", "PATH", true},
		{"unix_different_case", "linux", "PATH", "path", false},
		{"unix_different_name", "linux", "PATH", "HOME", false},
		{"windows_exact", "windows", "PATH", "PATH", true},
		{"windows_different_case", "windows", "PATH", "path", true},
		{"windows_mixed_case", "windows", "PaTh", "pAtH", true},
		{"windows_different_name", "windows", "PATH", "HOME", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			if got := equalEnvKey(tc.a, tc.b); got != tc.want {
				t.Errorf("equalEnvKey(%q,%q) goos=%s = %v, want %v", tc.a, tc.b, tc.goos, got, tc.want)
			}
		})
	}
}

// TestEnvLookup asserts envLookup obeys equalEnvKey's case-folding.
func TestEnvLookup(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/root", "malformed-no-equals", "EMPTY="}
	cases := []struct {
		name      string
		goos      string
		key       string
		wantVal   string
		wantFound bool
	}{
		{"unix_hit_exact", "linux", "PATH", "/usr/bin", true},
		{"unix_miss_wrong_case", "linux", "path", "", false},
		{"unix_miss_unknown", "linux", "FOO", "", false},
		{"unix_empty_value", "linux", "EMPTY", "", true},
		{"windows_hit_exact", "windows", "PATH", "/usr/bin", true},
		{"windows_hit_wrong_case", "windows", "path", "/usr/bin", true},
		{"windows_hit_mixed", "windows", "PaTh", "/usr/bin", true},
		{"windows_miss", "windows", "FOO", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			val, found := envLookup(env, tc.key)
			if found != tc.wantFound {
				t.Errorf("found=%v, want %v", found, tc.wantFound)
			}
			if val != tc.wantVal {
				t.Errorf("val=%q, want %q", val, tc.wantVal)
			}
		})
	}
}

// =============================================================================
// + - bash tool integration on POSIX (sanity)
// =============================================================================

// TestBash_UsesShellOnPOSIX runs bashTool on the host and asserts it
// still works after the selectShell refactor. Smoke that we didn't
// break the existing Unix path.
func TestBash_UsesShellOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only sanity test; Windows behaviour is asserted via shell-detection table")
	}
	bash := bashTool()
	out, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"printf 'shell-ok'"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "shell-ok") {
		t.Errorf("out=%q, want to contain shell-ok", out)
	}
}
