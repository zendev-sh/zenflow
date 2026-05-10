package tool

// relocation_test.go - evidence.
// Asserts that the CLI tool package now lives under
// `zenflow/cmd/zenflow/tool/` instead of `zenflow/tool/`. Per
// (separation of concerns): the zenflow library is pure orchestration;
// IO-touching tools (bash, read, write, glob, grep) belong to the CLI
// binary's own subpackage so library callers never import them
// transitively.
// The check is two-pronged so a future regression (e.g. someone
// resurrecting `zenflow/tool/`) is caught here first:
// 1. The legacy directory MUST NOT exist on disk.
// 2. The CLI tool catalog MUST still produce the expected 5-tool set
// under the new package path (smoke that the move is functionally
// intact, not just files relocated).

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCLITools_RelocatedSuccessfully asserts the CLI-tool layout
// invariant.
func TestCLITools_RelocatedSuccessfully(t *testing.T) {
	t.Run("legacy_zenflow_tool_dir_removed", func(t *testing.T) {
		// Walk up from this test file until we hit the `zenflow/` module
		// root, then check that `tool/` no longer sits beside it. We
		// can't hardcode an absolute path (CI runners use a different
		// checkout root) so we resolve via runtime.Caller.
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("runtime.Caller(0) failed - cannot locate test file")
		}
		// thisFile = .../zenflow/cmd/zenflow/tool/relocation_test.go
		// We want .../zenflow/tool - which must NOT exist.
		thisDir := filepath.Dir(thisFile)   // .../cmd/zenflow/tool
		cmdZenflow := filepath.Dir(thisDir) // .../cmd/zenflow
		cmdDir := filepath.Dir(cmdZenflow)  // .../cmd
		zenflowRoot := filepath.Dir(cmdDir) // .../zenflow (module root)
		legacyToolDir := filepath.Join(zenflowRoot, "tool")

		info, err := os.Stat(legacyToolDir)
		if err == nil {
			t.Fatalf("legacy directory %q still exists (mode=%s) - move regressed; CLI tools belong under zenflow/cmd/zenflow/tool/ only", legacyToolDir, info.Mode())
		}
		if !os.IsNotExist(err) {
			t.Fatalf("unexpected stat error on legacy dir %q: %v", legacyToolDir, err)
		}
	})

	t.Run("new_location_exposes_default_tool_set", func(t *testing.T) {
		tools := DefaultTools()
		if len(tools) == 0 {
			t.Fatal("DefaultTools() returned empty - relocation broke the catalog")
		}
		// The doc commits to the 5-tool catalog (bash, read, write,
		// glob, grep). If the count drifts, the design log + user-guide
		// reference must be updated together - fail loudly here.
		const want = 5
		if got := len(tools); got != want {
			names := make([]string, 0, got)
			for _, tl := range tools {
				names = append(names, tl.Name)
			}
			t.Fatalf("DefaultTools() len = %d, want %d - tools=%v", got, want, names)
		}
		expected := map[string]bool{"bash": true, "read": true, "write": true, "glob": true, "grep": true}
		for _, tl := range tools {
			if !expected[tl.Name] {
				t.Errorf("unexpected tool %q in DefaultTools() - catalog must be {bash,read,write,glob,grep}", tl.Name)
			}
			delete(expected, tl.Name)
		}
		for missing := range expected {
			t.Errorf("DefaultTools() missing %q", missing)
		}
	})
}
