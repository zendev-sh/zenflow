package exec

// public_api_stable_test.go asserts every Stable-tier exported `With*`
// helper carries a per-symbol `// Stable.` doc-comment marker.
// Maintenance contract: when promoting a `With*` helper from Experimental
// to Stable, add it to stableWithFunctions below AND add a `// Stable.`
// doc-comment marker on the function. When demoting, remove from both.
// The test fails loudly on either drift direction.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stableWithFunctions enumerates every With* helper classified Stable.
// Excludes:
// - WithVerbose (not in any Stable table)
// - WithStreamingBool, WithMailboxDeliveryBool (Deprecated wrappers - not
// part of the Stable surface; carry // Deprecated: doc comment only)
// - WithMaxWakeCycles, WithHoldTimeout, WithProgressBufferSize,
// WithRouterObserver (Experimental). `withClock` is package-private
// and is trivially excluded from the public stable list regardless.
// - WithTranscriptStore, WithMaxTranscriptMessages, WithMaxTranscriptBytes,
// WithTruncationOnCapReached (Experimental)
// - WithOutputTransform (Experimental - "will likely fold into a richer
// Compactor interface")
// - WithTracer (Experimental - "may add domain methods later")
var stableWithFunctions = []string{
	// Coordinator wiring (audit §"Coordinator wiring")
	"WithCoordinator",
	// Per-call options (audit §"Per-call options")
	"WithFlowContext",
	"WithGoalContext",
	// Common Orchestrator options (audit §"Common Orchestrator options")
	"WithModel",
	"WithDefaultModel",
	"WithModelResolver",
	"WithTools",
	"WithGoAIOptions",
	"WithStorage",
	"WithProgress",
	"WithStreaming",
	"WithoutStreaming",
	"WithMaxConcurrency",
	"WithMaxTurns",
	"WithMaxDepth",
	"WithApproval",
	"WithApprovalTimeout",
	"WithPermissions",
	"WithSharedMemory",
	"WithIsolation",
	"WithRunID",
	// Routing primitives (audit §"Routing primitives")
	"WithMailboxStore",
	"WithMaxMailboxSize",
	"WithMailboxDelivery",
	"WithoutMailboxDelivery",
	"WithExternalInbox",
	// Drop telemetry (audit §"Drop telemetry")
	"WithDropCallback",
	"WithDropCallbackBufferSize",
	// Coord runner construction (CoordOption)
	"WithCoordContextProvider",
	// Runner-level surface (AgentRunnerOption); twin of WithCoordContextProvider
	"WithRunnerWakeContextProvider",
}

// TestPublicAPI_AllStableOptionsHaveMarker enumerates the ground-truth
// Stable With* functions and asserts each one (a) exists in the package
// AST and (b) has a `// Stable.` line in its leading doc-comment block.
func TestPublicAPI_AllStableOptionsHaveMarker(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	fset := token.NewFileSet()
	// parser.ParseDir + ast.Package were deprecated in Go 1.22/1.25 in
	// favour of golang.org/x/tools/go/packages. This audit only needs
	// surface-level package names so the heavier dependency is not
	// justified; pin the deprecated path with a targeted nolint.
	pkgs, err := parser.ParseDir(fset, wd, func(fi os.FileInfo) bool { //nolint:staticcheck // SA1019: deliberate; see comment above
 // Skip _test.go files - only audit production source.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse exec package: %v", err)
	}

	pkg, ok := pkgs["exec"]
	if !ok {
		t.Fatalf("exec package not found in %s; got: %v", wd, parsedPkgNames(pkgs))
	}

	// Index every top-level FuncDecl by name -> *ast.FuncDecl so we can
	// check both presence and doc-comment text.
	funcs := map[string]*ast.FuncDecl{}
	for fname, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn || fn.Recv != nil {
				continue
			}
			funcs[fn.Name.Name] = fn
			_ = fname
		}
	}

	var missing []string
	var notFound []string
	for _, name := range stableWithFunctions {
		fn, exists := funcs[name]
		if !exists {
			notFound = append(notFound, name)
			continue
		}
		if !hasStableMarker(fn.Doc) {
			pos := fset.Position(fn.Pos())
			missing = append(missing, name+" ("+filepath.Base(pos.Filename)+":"+formatLine(pos.Line)+")")
		}
	}

	if len(notFound) > 0 {
		t.Errorf("Stable With* functions listed in stableWithFunctions but not present in zenflow package source:\n  %s\n\n"+
			"Either remove from stableWithFunctions OR add the function back to the package.",
			strings.Join(notFound, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("Stable With* functions WITHOUT `// Stable.` doc-comment marker (%d):\n  %s\n\n"+
			"Either add `// Stable.` line to each function's doc-comment block, OR\n"+
			"if no longer Stable, remove from stableWithFunctions.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// hasStableMarker returns true when the comment group contains a line
// equal to "Stable." (after the leading "// " is stripped) - same form
// the codebase already uses on Option, RunFlowOption, etc.
func hasStableMarker(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
 // c.Text always starts with "//" or "/*". Strip the leading
 // "// " or "//" to get the bare line.
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), " "))
		if line == "Stable." {
			return true
		}
	}
	return false
}

// formatLine avoids strconv import for a one-off line-number formatter.
func formatLine(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// parsedPkgNames returns the keys of a map[string]*ast.Package as []string
// for error messages. Local helper to avoid pulling slices/maps just for this.
func parsedPkgNames(m map[string]*ast.Package) []string { //nolint:staticcheck // SA1019: ast.Package paired with parser.ParseDir above
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
