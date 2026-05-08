//go:build e2e

package exec

// e2eEnabled toggles e2e-only test scaffolding (currently the HTTP/2
// goleak ignores in TestMain). Set to true when the e2e build tag is
// active so unit-only test runs do NOT silently mask real HTTP/2
// transport leaks.
const e2eEnabled = true
