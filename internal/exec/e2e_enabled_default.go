//go:build !e2e

package exec

// e2eEnabled toggles e2e-only test scaffolding (currently the HTTP/2
// goleak ignores in TestMain). Defaults to false in unit-test builds so
// any newly introduced HTTP/2 leak surfaces as a failure instead of
// being masked by an e2e-scoped ignore.
const e2eEnabled = false
