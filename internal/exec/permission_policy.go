// Package exec - permission policy decision logic.
// This file extracts the pure decision logic from the CLI permission handler
// (cmd/zenflow/permission.go) so library consumers can reuse the same yolo /
// allow / deny / strict / sandbox semantics without re-implementing them.
// The CLI binary still owns the interactive prompt UI, the alwaysAllow state,
// and TTY detection. This file owns ONLY the policy decision.

package exec

import (
	"errors"
	"fmt"
)

// ErrToolDenied is returned (wrapped) by DecidePermission when a tool
// matches the policy's Deny list. Callers may match via errors.Is to
// distinguish deny-flag rejections from strict-mode rejections.
// Stable.
var ErrToolDenied = errors.New("zenflow: tool denied by --deny flag")

// ErrToolNotAllowed is returned (wrapped) by DecidePermission when a
// strict-mode policy rejects a tool that is not on the Allow list.
// Callers may match via errors.Is.
// Stable.
var ErrToolNotAllowed = errors.New("zenflow: tool not in --allow list (--strict mode)")

// SandboxDefaultAllow returns the canonical safe-tool allow-list applied
// by --sandbox: read, write, grep, glob. bash is intentionally absent -
// the whole point of sandbox mode is to block shell access. Callers may
// extend with --allow, but bash remains blocked even then (sandbox wins).
// Re-exported at the root facade as zenflow.SandboxDefaultAllow. Returns
// a fresh slice on each call so callers cannot mutate the canonical list.
// Stable.
func SandboxDefaultAllow() []string {
	return []string{"read", "write", "grep", "glob"}
}

// PermissionPolicy captures the static permission-related flags parsed from
// the CLI (or constructed by a library consumer). It is a value type - the
// caller owns the mutable per-run state (alwaysAllow map, prompt UI).
// Decision precedence is encoded by DecidePermission and matches the
// behavior previously inlined in cliPermissionHandler.RequestPermission:
// 1. Yolo → allow.
// 2. Deny match → deny (error).
// 3. Allow match → allow.
// 4. Strict + no allow match → deny (error).
// 5. AlwaysAllow hit → allow.
// 6. Otherwise → caller should prompt.
type PermissionPolicy struct {
	Yolo   bool     // allow every tool, no prompt (YOLO mode)
	Allow  []string // pre-allowed tool names
	Deny   []string // pre-denied tool names
	Strict bool     // deny anything not on Allow list (no prompt)
	// Sandbox is a parse-time concept - by the time a policy reaches
	// DecidePermission, sandbox semantics have already been folded into
	// Strict + Allow. The field is preserved for round-tripping / debug.
	Sandbox bool
}

// DecidePermission applies the policy to a single tool call.
// Return values:
// - allowed=true, prompt=false, err=nil → run the tool.
// - allowed=false, prompt=false, err!=nil → reject; err is the user-facing
// reason (deny match, strict no-match).
// - allowed=false, prompt=true, err=nil → policy is silent; caller must
// decide (typically: ask the user, or deny on non-TTY).
// alwaysAllow may be nil; a nil map is treated as empty.
func DecidePermission(p PermissionPolicy, toolName string, alwaysAllow map[string]bool) (allowed, prompt bool, err error) {
	if p.Yolo {
		return true, false, nil
	}
	if containsToolName(p.Deny, toolName) {
		return false, false, fmt.Errorf("tool %q: %w", toolName, ErrToolDenied)
	}
	if containsToolName(p.Allow, toolName) {
		return true, false, nil
	}
	if p.Strict {
		return false, false, fmt.Errorf("tool %q: %w", toolName, ErrToolNotAllowed)
	}
	if alwaysAllow != nil && alwaysAllow[toolName] {
		return true, false, nil
	}
	return false, true, nil
}

// containsToolName is a short-circuit linear search; the lists carried by
// PermissionPolicy are tiny (single-digit), so a map would be over-
// engineering. Named to avoid colliding with the (string, string) helpers
// already in the package's test suite.
func containsToolName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}
