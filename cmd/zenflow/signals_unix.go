//go:build !windows

package main

import (
	"os"
	"syscall"
)

// platformPosixSignals returns the POSIX-only shutdown signal set:
// SIGINT (Ctrl+C), SIGTERM (orchestrator shutdown), SIGHUP (terminal
// hangup). Lives in a build-tag-gated file because syscall.SIGTERM /
// SIGHUP are unavailable on Windows and would fail to compile there.
// platformShutdownSignals (in signals.go) routes here when goos !=
// "windows". Tests cannot directly exercise this on Windows CI, but the
// goos-flip test in signals_test.go asserts the dispatch logic chooses
// the right helper.
func platformPosixSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
