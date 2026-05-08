package main

import (
	"os"
	"sync/atomic"
)

// ANSI color/style codes.
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
)

// Semantic 24-bit truecolor palette. Modern terminal emulators
// (iTerm2, kitty, alacritty, VSCode, Windows Terminal, etc.) all support
// the 38;2;R;G;B escape; legacy 8-color terminals ignore it gracefully
// (the text still renders, just without color).
const (
	Success  = "\033[38;2;34;197;94m"   // #22c55e
	ErrorFG  = "\033[38;2;239;68;68m"   // #ef4444
	Warning  = "\033[38;2;234;179;8m"   // #eab308
	Info     = "\033[38;2;59;130;246m"  // #3b82f6
	Thinking = "\033[38;2;124;58;237m"  // #7c3aed
	Accent   = "\033[38;2;45;74;123m"   // #2d4a7b
	Muted    = "\033[38;2;164;164;164m" // #a4a4a4
)

// colorEnabled is computed once at package init and cached. Override in tests
// via SetColorEnabled. Uses atomic.Bool to prevent data races when tests call
// SetColorEnabled concurrently with production reads from C.
var colorEnabled atomic.Bool

// stdoutStat is the function used to stat stdout. Replaceable in tests to
// exercise the stat-error branch of computeColorEnabled.
var stdoutStat = func() (os.FileInfo, error) { return os.Stdout.Stat() }

func init() {
	colorEnabled.Store(computeColorEnabled())
}

func computeColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := stdoutStat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// ColorEnabled reports whether ANSI color output should be used. Computed
// once at package init. Returns false when NO_COLOR is set, TERM=dumb, or
// stdout is not a terminal.
func ColorEnabled() bool { return colorEnabled.Load() }

// SetColorEnabled overrides the cached value.
// Stable.
func SetColorEnabled(v bool) { colorEnabled.Store(v) }

// C wraps text with an ANSI style code if color is enabled.
// Returns plain text when color is disabled.
func C(style, text string) string {
	if !ColorEnabled() {
		return text
	}
	return style + text + Reset
}
