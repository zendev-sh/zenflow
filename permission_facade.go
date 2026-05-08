package zenflow

// permission_facade.go re-exports the permission policy decision surface
// from internal/exec so library consumers (and the standalone CLI) share
// one source of truth for yolo / allow / deny / strict semantics. The
// interactive prompt UI and the alwaysAllow state remain CLI-side; this
// facade exposes only the pure decision logic and the canonical sandbox
// safe-tool set.

import "github.com/zendev-sh/zenflow/internal/exec"

// PermissionPolicy is re-exported from internal/exec.
type PermissionPolicy = exec.PermissionPolicy

// SandboxDefaultAllow is re-exported from internal/exec - the canonical
// safe-tool allow-list applied by --sandbox (read, write, grep, glob).
// bash is intentionally absent. Returns a fresh slice on each call so
// callers cannot mutate the canonical list.
var SandboxDefaultAllow = exec.SandboxDefaultAllow

// DecidePermission is re-exported from internal/exec. Applies a static
// PermissionPolicy plus the caller-owned alwaysAllow map and returns
// (allowed, prompt, err): see exec.DecidePermission docs.
var DecidePermission = exec.DecidePermission

// ErrToolDenied is re-exported from internal/exec. Wrapped by
// DecidePermission when a tool matches the policy's Deny list.
var ErrToolDenied = exec.ErrToolDenied

// ErrToolNotAllowed is re-exported from internal/exec. Wrapped by
// DecidePermission when a strict-mode policy rejects a tool not on
// the Allow list.
var ErrToolNotAllowed = exec.ErrToolNotAllowed
