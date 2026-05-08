//go:build windows

package main

import "os"

// platformPosixSignals returns nothing on Windows - the POSIX-only
// signals (SIGTERM, SIGHUP) don't exist as deliverable signals on
// Windows even though Go's syscall package exposes some of the
// constants. platformShutdownSignals's `goos == "windows"` branch
// short-circuits before this is called in production, but we provide
// the symbol so signals.go compiles on Windows.
// Returning an empty slice here would cause installSignalHandler to
// register a no-op signal.Notify (which would block forever); the
// signals.go dispatcher already short-circuits the windows case and
// uses [os.Interrupt] directly, so this function is intentionally
// unreachable in production. It exists only to keep the cross-platform
// signature of `platformPosixSignals` symmetric.
func platformPosixSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
