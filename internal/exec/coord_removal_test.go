package exec

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestCoordinatorTypesRemoved asserts that the legacy coordinator
// type identifiers - LLMCoordinator, NoopCoordinator, CoordinatorAgent,
// CoordinatorTokens, CoordinatorMode - no longer appear in any non-test
// .go file inside the zenflow/ package. Tests are explicitly excluded
// because the helpers used to validate the new design may legitimately
// reference the old names in commentary; this gate is about source code
// declarations and references, not test prose.
// (Fix 9): explicit exit-code check so a real grep error
// (e.g. exit 2 - invalid regex, missing tool, permission error) is
// distinguished from "no match" (exit 1, our success case).
func TestCoordinatorTypesRemoved(t *testing.T) {
	cmd := exec.Command("grep", "-rn",
		"LLMCoordinator\\|NoopCoordinator\\|CoordinatorAgent\\|CoordinatorTokens\\|CoordinatorMode",
		"--include=*.go",
		".",
	)
	out, err := cmd.Output()
	// grep exit codes: 0 = match found (we filter below), 1 = no match
	// (PASS for this gate), 2+ = real error (FAIL).
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 1 {
		t.Fatalf("grep failed with exit code %d: %v", exitErr.ExitCode(), err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var hits []string
	for _, line := range lines {
		if line == "" {
			continue
		}
 // Path is first colon-separated field.
		path := line
		if i := strings.Index(line, ":"); i >= 0 {
			path = line[:i]
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
 // Vendored copies of the observability sub-module live under
 // vendor/ - they are external to the package being refactored
 // and only carry a stale doc comment. Exclude them.
		if strings.HasPrefix(path, "./vendor/") || strings.HasPrefix(path, "vendor/") {
			continue
		}
		hits = append(hits, line)
	}
	if len(hits) > 0 {
		t.Fatalf("expected zero non-test references to legacy coordinator types, got %d:\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}

// TestCoordinatorModeOptionRemoved asserts WithCoordinatorMode
// is gone from the package surface. The grep above already covers the
// type CoordinatorMode; this test specifically targets the option
// constructor name in case a stale stub is left behind.
func TestCoordinatorModeOptionRemoved(t *testing.T) {
	cmd := exec.Command("grep", "-rn", "WithCoordinatorMode",
		"--include=*.go", ".")
	out, err := cmd.Output()
	// (Fix 9): same grep exit-code discipline as above.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 1 {
		t.Fatalf("grep failed with exit code %d: %v", exitErr.ExitCode(), err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var hits []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		path := line
		if i := strings.Index(line, ":"); i >= 0 {
			path = line[:i]
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		if strings.HasPrefix(path, "./vendor/") || strings.HasPrefix(path, "vendor/") {
			continue
		}
		hits = append(hits, line)
	}
	if len(hits) > 0 {
		t.Fatalf("expected zero non-test references to WithCoordinatorMode, got %d:\n%s",
			len(hits), strings.Join(hits, "\n"))
	}
}
