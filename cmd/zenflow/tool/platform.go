// Cross-platform helpers for (Windows support).
// All branching on operating system goes through the `goos` package
// variable so tests can exercise both Unix and Windows code paths from
// any host (we develop on macOS but the CLI ships to Windows too).
// Things that genuinely cannot be exercised cross-platform (e.g.
// syscall.SysProcAttr.CreationFlags is only defined on Windows) live in
// `_windows.go` files behind `//go:build windows` and have build-tagged
// tests that run only on Windows CI. Everything else uses the `goos`
// indirection.
package tool

import (
	"path/filepath"
	"runtime"
	"strings"
)

// goos shadows runtime.GOOS so tests can simulate Windows behaviour on
// non-Windows hosts. Production reads this once at init via runtime.GOOS;
// tests write to it inside `defer restore` blocks. Never write to it
// from production code.
var goos = runtime.GOOS

// selectShell returns the shell binary and its switch-to-script flag for
// the current platform.
// - Unix-like (linux, darwin, *bsd): `sh`, `-c`. The PowerShell branch
// never runs there - `sh` is universally available.
// - Windows: `powershell.exe`, `-Command`. We deliberately prefer
// PowerShell over cmd.exe because LLM-emitted commands routinely
// use POSIX-ish constructs (pipes, `&&`) that PowerShell handles
// natively but cmd.exe handles poorly. PowerShell is bundled with
// every supported Windows release.
// The returned shell string is the bare executable name; exec.LookPath
// resolves it on PATH at spawn time. Callers should pass the user
// command verbatim as the next argv element - see wrapCommand below for
// any additional escaping required by the shell.
func selectShell() (shell string, flag string) {
	if goos == "windows" {
		return "powershell.exe", "-Command"
	}
	return "sh", "-c"
}

// wrapCommand prepares the user-supplied command string for the chosen
// shell. POSIX `sh -c '<cmd>'` accepts the command verbatim - no
// translation needed. PowerShell accepts the command verbatim too, but
// we strip leading/trailing whitespace so a stray newline in an LLM
// response doesn't produce an empty pipeline error. PowerShell's
// `-Command` parses the rest of argv as a single script string, so the
// caller passes the result of wrapCommand as one argv element.
// We do NOT translate POSIX syntax (`&&`, `|`, `$VAR`) into PowerShell
// syntax. PowerShell since v7 understands `&&` as "stop-on-error
// chaining" natively, and `|` and `$env:VAR` resemble POSIX closely
// enough that simple commands work out of the box. Complex commands are
// the LLM's responsibility - it can detect Windows from the system
// prompt and emit native PowerShell instead.
func wrapCommand(cmd string) string {
	if goos == "windows" {
		return strings.TrimSpace(cmd)
	}
	return cmd
}

// normalizePath converts a user-supplied path to the platform's native
// separator + cleans it. On Windows, `filepath.Clean` already swaps `/`
// for `\` and collapses `..` segments. On Unix it's a straight Clean.
// Drive-letter handling: on Windows, an absolute path like `C:\foo` is
// preserved verbatim by Clean. A path like `C:foo` (drive-relative) is
// also preserved - `filepath.IsAbs` returns false for it, which is the
// existing zenflow behaviour for sandbox containment (drive-relative
// paths are joined onto workdir).
// The returned path is suitable to pass directly to os.Open / os.Stat /
// exec.Command without further translation.
func normalizePath(p string) string {
	if goos == "windows" {
		// filepath.FromSlash converts `/` → `\` on Windows; on Unix it
		// is a no-op. We call it explicitly so the macOS-side test that
		// flips goos="windows" doesn't depend on filepath using the host
		// separator - we want the *intent* of "normalize for Windows"
		// to be visible.
		return filepath.Clean(filepath.FromSlash(p))
	}
	return filepath.Clean(p)
}

// equalEnvKey compares two environment variable names with
// platform-appropriate case sensitivity. Windows env var names are
// case-insensitive (`PATH` == `path` == `Path`); POSIX env var names
// are case-sensitive. Callers comparing user-supplied env keys against
// known names (e.g. PATH for shell discovery) should use this rather
// than `==` so the same code path works on both OSes.
func equalEnvKey(a, b string) bool {
	if goos == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// envLookup searches a slice of `KEY=VALUE` env strings (the same shape
// os.Environ returns) for a given key, with platform-appropriate
// case sensitivity. Returns the value and true if found; "", false
// otherwise.
// This is a pure helper so callers building a custom os/exec.Cmd.Env
// can do platform-correct lookups without rolling their own loop. We
// don't expose it as a wrapper around os.Getenv because os.Getenv
// already does case-insensitive lookup on Windows internally - the gap
// is in user-built env slices, where Go's stdlib does NOT
// case-fold.
func envLookup(env []string, key string) (string, bool) {
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if equalEnvKey(kv[:eq], key) {
			return kv[eq+1:], true
		}
	}
	return "", false
}
