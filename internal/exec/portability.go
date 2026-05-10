package exec

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// PortabilityWarning carries advisory findings from LintPortability.
type PortabilityWarning struct {
	Field   string // dotted path (e.g. "steps[3].instructions")
	Kind    string // "absolute-path"
	Message string
	Snippet string // offending substring (trimmed)
}

// HostSpecificEnvError indicates a workflow hard-codes a host-specific
// env variable that prevents portability across machines.
// Stable.
type HostSpecificEnvError struct {
	Field string
	Var   string
}

func (e *HostSpecificEnvError) Error() string {
	// Error string omits internal identifiers; end users have no way
	// to look them up.
	return fmt.Sprintf("zenflow: host-specific env interpolation $%s in %s", e.Var, e.Field)
}

// absolutePathRe matches a POSIX absolute path or Windows drive.
// The regex is deliberately conservative: it only flags ^/ or ^[A-Za-z]:\
// as the START of a standalone token to minimise false positives on
// prose like "See /tmp for details".
var absolutePathRe = regexp.MustCompile(`(?m)(^|[\s"'=])(/[A-Za-z0-9_./-]{3,}|[A-Za-z]:\\[A-Za-z0-9_.\\-]+)`)

// hostEnvRe matches the set of host-specific env vars that must be
// rejected. These identify a user/machine and therefore break
// portability.
var hostSpecificEnvVars = []string{
	"USER", "USERNAME", "HOSTNAME", "HOME", "PWD", "LOGNAME",
	"OLDPWD", "SHELL", "TMPDIR",
}

// hostEnvRe compiles `\$(USER|HOSTNAME|...)\b` or `\$\{USER\}`.
var hostEnvRe = regexp.MustCompile(
	`\$\{?(` + strings.Join(hostSpecificEnvVars, "|") + `)\}?\b`,
)

// LintPortability inspects a parsed workflow and returns:
// - warnings: absolute paths spotted in instructions / descriptions
// / default values; non-fatal.
// - err: the first host-specific env interpolation encountered;
// fatal - callers should refuse to load the workflow.
// Both return values are independent: a workflow can have warnings
// and no error.
func LintPortability(wf *Workflow) (warnings []PortabilityWarning, err error) {
	if wf == nil {
		return nil, nil
	}
	visit := func(field, text string) error {
		if text == "" {
			return nil
		}
		if m := hostEnvRe.FindStringSubmatch(text); m != nil {
			return &HostSpecificEnvError{Field: field, Var: m[1]}
		}
		for _, loc := range absolutePathRe.FindAllStringIndex(text, -1) {
			snippet := strings.TrimSpace(text[loc[0]:loc[1]])
			warnings = append(warnings, PortabilityWarning{
				Field:   field,
				Kind:    "absolute-path",
				Message: "absolute filesystem path is not portable across machines",
				Snippet: snippet,
			})
		}
		return nil
	}

	if e := visit("description", wf.Description); e != nil {
		return warnings, e
	}
	for i, s := range wf.Steps {
		base := fmt.Sprintf("steps[%d]", i)
		if e := visit(base+".instructions", s.Instructions); e != nil {
			return warnings, e
		}
		for j, cf := range s.ContextFiles {
			if e := visit(fmt.Sprintf("%s.contextFiles[%d]", base, j), cf); e != nil {
				return warnings, e
			}
		}
	}
	for name, agent := range wf.Agents {
		if e := visit(fmt.Sprintf("agents[%q].prompt", name), agent.Prompt); e != nil {
			return warnings, e
		}
		if e := visit(fmt.Sprintf("agents[%q].description", name), agent.Description); e != nil {
			return warnings, e
		}
	}
	return warnings, nil
}

// --- Unicode sanitization ---

// UnicodeUnsafeError indicates a string contains a disallowed code point.
// Stable.
type UnicodeUnsafeError struct {
	Reason string
	Rune   rune
}

func (e *UnicodeUnsafeError) Error() string {
	return fmt.Sprintf("zenflow: unicode-unsafe (%s): %U", e.Reason, e.Rune)
}

// bidi-override code points are prohibited as they enable rtl-overlay
// attacks on rendered text.
func isBidiOverride(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// isStrippableControl reports whether r is a C0/C1 control that should
// be removed (except LF and TAB, which are legitimate in YAML text).
func isStrippableControl(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	if r < 0x20 || (r >= 0x7F && r < 0xA0) {
		return true
	}
	return false
}

// isZeroWidth reports whether r is a zero-width / invisible formatting
// code point. These pass the `!IsPrint && !IsSpace` filter in
// SanitizeUnicode because unicode.IsSpace reports ZWSP/ZWNBSP as space,
// yet they are invisible to humans and can smuggle prompt-injection
// payloads past operator review.
//
//	U+200B ZERO WIDTH SPACE
//	U+200C ZERO WIDTH NON-JOINER
//	U+200D ZERO WIDTH JOINER
//	U+2060 WORD JOINER
//	U+FEFF ZERO WIDTH NO-BREAK SPACE (BOM when leading)
func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF:
		return true
	}
	return false
}

// isPrivateUse reports whether r lies in a Unicode Private Use Area.
// unicode.IsPrint returns true for these because the general category
// is Co (Private Use), which is considered graphic; but the glyphs
// themselves carry no assigned meaning and are a common channel for
// smuggling invisible / homoglyph payloads into LLM prompts.
//
//	U+E000..U+F8FF BMP PUA
//	U+F0000..U+FFFFD Supplementary Private Use Area-A
//	U+100000..U+10FFFD Supplementary Private Use Area-B
func isPrivateUse(r rune) bool {
	switch {
	case r >= 0xE000 && r <= 0xF8FF:
		return true
	case r >= 0xF0000 && r <= 0xFFFFD:
		return true
	case r >= 0x100000 && r <= 0x10FFFD:
		return true
	}
	return false
}

// SanitizeUnicode returns an NFC-normalised copy of s with C0/C1
// controls stripped. It returns UnicodeUnsafeError when a bidi-override
// code point is present - the caller should refuse the input.
func SanitizeUnicode(s string) (string, error) {
	// Fast path: pure ASCII without controls.
	if isSafeASCII(s) {
		return s, nil
	}
	// Reject bidi-overrides up front.
	for _, r := range s {
		if isBidiOverride(r) {
			return "", &UnicodeUnsafeError{Reason: "bidi-override", Rune: r}
		}
	}
	// Strip controls + NFC normalise.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isStrippableControl(r) {
			continue
		}
		// Strip zero-width formatting characters (Unicode bidi/format
		// chars commonly used to smuggle hidden content into prompts).
		// They are invisible to humans but survive the IsPrint/IsSpace
		// gate (ZWSP etc. report IsSpace=true), so a workflow author
		// can sneak one into an agent prompt to alter LLM behaviour
		// without any visible cue in a code review.
		if isZeroWidth(r) {
			continue
		}
		// Strip Private Use Area code points. IsPrint is true for Co,
		// but the glyphs carry no assigned meaning and are a known
		// vector for prompt-injection smuggling in LLM context.
		if isPrivateUse(r) {
			continue
		}
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			// Other non-printable (e.g. format chars U+2060). Drop.
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String()), nil
}

// DetectMixedScript reports whether s contains code points from two or
// more distinct Unicode scripts (e.g. Latin + Cyrillic). It is a pure
// observability helper - callers log a warning but do NOT reject the
// input, because legitimate multilingual workflows exist.
// The detector only considers scripts that frequently host homoglyph
// attacks: Latin, Cyrillic, Greek, Armenian, Hebrew, Arabic, Han.
// Common/Inherited (digits, punctuation, combining marks) and scripts
// outside that set do not trigger the signal so the false-positive
// rate stays low.
func DetectMixedScript(s string) bool {
	seen := 0
	const (
		latin    = 1 << iota // iota=0
		cyrillic             // iota=1
		greek
		armenian
		hebrew
		arabic
		han
	)
	for _, r := range s {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= 0x00C0 && r <= 0x024F): // Latin + supplements
			seen |= latin
		case r >= 0x0400 && r <= 0x04FF: // Cyrillic
			seen |= cyrillic
		case r >= 0x0370 && r <= 0x03FF: // Greek and Coptic
			seen |= greek
		case r >= 0x0530 && r <= 0x058F: // Armenian
			seen |= armenian
		case r >= 0x0590 && r <= 0x05FF: // Hebrew
			seen |= hebrew
		case r >= 0x0600 && r <= 0x06FF: // Arabic
			seen |= arabic
		case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
			seen |= han
		}
		// popcount ≥ 2 ⇒ mixed.
		if seen != 0 && seen&(seen-1) != 0 {
			return true
		}
	}
	return false
}

func isSafeASCII(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' {
			return false
		}
		if c >= 0x7F {
			return false
		}
	}
	return true
}
