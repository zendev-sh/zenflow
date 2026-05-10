package exec

import (
	"errors"
	"strings"
	"testing"
)

// TestPortability_FlagsAbsolutePath - ZF8.0f.i warning path.
func TestPortability_FlagsAbsolutePath(t *testing.T) {
	wf := &Workflow{
		Description: "loads /home/user/data.csv as input",
		Steps: []Step{
			{ID: "a", Instructions: "read from /opt/input/foo.json"},
		},
	}
	warns, err := LintPortability(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) < 2 {
		t.Fatalf("expected ≥2 warnings, got %d: %+v", len(warns), warns)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w.Snippet, "/home/user/data.csv") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("absolute path warning missing: %+v", warns)
	}
}

// TestPortability_RejectsHostEnv - ZF8.0f.i error path.
func TestPortability_RejectsHostEnv(t *testing.T) {
	cases := []string{
		"read $HOME/config.yaml",
		"token: ${USER}_token",
		"fetch from $HOSTNAME",
	}
	for _, c := range cases {
		c := c
		t.Run(c, func(t *testing.T) {
			wf := &Workflow{Description: c, Steps: []Step{{ID: "a", Instructions: "x"}}}
			_, err := LintPortability(wf)
			if err == nil {
				t.Errorf("case %q should return error", c)
				return
			}
			var he *HostSpecificEnvError
			if !errors.As(err, &he) {
				t.Errorf("case %q: wrong error type: %T", c, err)
			}
		})
	}
}

// TestPortability_CleanWorkflowPasses - no warnings, no error.
func TestPortability_CleanWorkflowPasses(t *testing.T) {
	wf := &Workflow{
		Description: "relative workflow, uses ./input.json",
		Steps:       []Step{{ID: "a", Instructions: "read ./input.json and emit output"}},
	}
	warns, err := LintPortability(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %+v", warns)
	}
}

// TestPortability_NilSafe - lint tolerates nil workflow.
func TestPortability_NilSafe(t *testing.T) {
	warns, err := LintPortability(nil)
	if err != nil || warns != nil {
		t.Fatalf("nil should be no-op: warns=%v err=%v", warns, err)
	}
}

// TestUnicodeSanitize_StripsControls - ZF8.hard.4.
func TestUnicodeSanitize_StripsControls(t *testing.T) {
	in := "hello\x00world\x07"
	got, err := SanitizeUnicode(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "helloworld" {
		t.Fatalf("got %q", got)
	}
}

// TestUnicodeSanitize_RejectsBidiOverride - ZF8.hard.4.
func TestUnicodeSanitize_RejectsBidiOverride(t *testing.T) {
	for _, r := range []rune{0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069} {
		in := "hi" + string(r) + "evil"
		if _, err := SanitizeUnicode(in); err == nil {
			t.Errorf("rune %U should be rejected", r)
		}
	}
}

// TestUnicodeSanitize_NFC - combining marks are normalised.
func TestUnicodeSanitize_NFC(t *testing.T) {
	// "é" as composed + as NFD (e + combining acute).
	composed := "\u00e9"
	decomposed := "e\u0301"
	got, err := SanitizeUnicode(decomposed)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != composed {
		t.Fatalf("NFC failed: got %q (% x), want %q", got, got, composed)
	}
}

// TestUnicodeSanitize_PreservesLFTab - legitimate whitespace survives.
func TestUnicodeSanitize_PreservesLFTab(t *testing.T) {
	in := "line1\nline2\tcolumn"
	got, err := SanitizeUnicode(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != in {
		t.Fatalf("whitespace mutated: %q", got)
	}
}

// TestSanitizeUnicode_StripsZeroWidth verifies that zero-width code points
// pass the IsPrint/IsSpace gate (e.g. unicode.IsSpace(ZWSP) == true) so
// the original implementation let them survive into LLM prompts where
// they are invisible in code review but change tokenisation.
func TestSanitizeUnicode_StripsZeroWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ZWSP between words", "hello\u200Bworld", "helloworld"},
		{"ZWNJ trailing", "admin\u200C", "admin"},
		{"ZWJ between letters", "a\u200Db", "ab"},
		{"word-joiner inside", "role:\u2060user", "role:user"},
		{"BOM leading", "\uFEFFsudo", "sudo"},
		{"all zero-widths mixed",
			"safe\u200B\u200C\u200D\u2060\uFEFFinput",
			"safeinput",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := SanitizeUnicode(c.in)
			if err != nil {
				t.Errorf("%s: err=%v", c.name, err)
				return
			}
			if got != c.want {
				t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestSanitizeUnicode_StripsPrivateUse verifies that private-use code points
// are Co in Go's unicode.IsPrint sense (printable) but carry no
// assigned meaning and are a known prompt-injection channel.
func TestSanitizeUnicode_StripsPrivateUse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"BMP PUA start", "a\uE000b", "ab"},
		{"BMP PUA end", "a\uF8FFb", "ab"},
		{"SPUA-A", "a" + string(rune(0xF0000)) + "b", "ab"},
		{"SPUA-B", "a" + string(rune(0x10FFFD)) + "b", "ab"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := SanitizeUnicode(c.in)
			if err != nil {
				t.Errorf("%s: err=%v", c.name, err)
				return
			}
			if got != c.want {
				t.Errorf("%s: got %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestDetectMixedScript is an observability helper; verify
// positive (Latin+Cyrillic) and negative (pure ASCII, pure Cyrillic,
// Latin+digits+punct) branches.
func TestDetectMixedScript(t *testing.T) {
	mixed := []string{
		"pаypal", // Latin 'p' + Cyrillic 'а' (U+0430)
		"αdmin",  // Greek α + Latin
		"中文mix",  // Han + Latin
	}
	pure := []string{
		"admin",
		"привет", // pure Cyrillic
		"v1.2.3-rc.1",
		"",
		"file_name-42",
	}
	for _, s := range mixed {
		if !DetectMixedScript(s) {
			t.Errorf("expected mixed: %q", s)
		}
	}
	for _, s := range pure {
		if DetectMixedScript(s) {
			t.Errorf("expected pure: %q", s)
		}
	}
}

// TestPortability_WarnsInAgentPromptWithHostEnv - combined coverage.
func TestPortability_WarnsInAgentPromptWithHostEnv(t *testing.T) {
	wf := &Workflow{
		Description: "clean",
		Steps:       []Step{{ID: "a", Instructions: "clean"}},
		Agents: map[string]AgentConfig{
			"bot": {Description: "writes to $HOME/out.txt", Prompt: "p"},
		},
	}
	_, err := LintPortability(wf)
	if err == nil {
		t.Fatal("expected host-env error via agent description")
	}
}

// TestSanitizeUnicode_StripsNonPrintNonSpaceFormat - coverage
// for the fall-through branch that drops code points which pass the
// bidi / control / zero-width / private-use filters but still satisfy
// (!IsPrint && !IsSpace). U+2061 INVISIBLE TIMES is the canonical
// example: it is a Cf format character, not in the zero-width set we
// enumerate, not in a PUA block, and not a control - but it is still
// invisible to humans and must be stripped so LLM prompts cannot hide
// payloads behind it.
func TestSanitizeUnicode_StripsNonPrintNonSpaceFormat(t *testing.T) {
	in := "a\u2061b\u2062c"
	got, err := SanitizeUnicode(in)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
}

// TestDetectMixedScript_RareScripts - coverage for the
// Armenian / Hebrew / Arabic branches of the script-classification
// switch. Each case pairs one of these scripts with Latin so the
// mixed-script signal must fire.
func TestDetectMixedScript_RareScripts(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		// U+0531 ARMENIAN CAPITAL LETTER AYB + Latin.
		{"armenian+latin", "a\u0531b"},
		// U+05D0 HEBREW LETTER ALEF + Latin.
		{"hebrew+latin", "a\u05D0b"},
		// U+0628 ARABIC LETTER BEH + Latin.
		{"arabic+latin", "a\u0628b"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if !DetectMixedScript(c.in) {
				t.Errorf("%s: expected mixed, got pure for %q", c.name, c.in)
			}
		})
	}
	// Pure-script negatives for the same three scripts - confirms the
	// branch sets the bit without spuriously flipping another.
	pure := []string{
		"\u0531\u0532\u0533", // pure Armenian
		"\u05D0\u05D1\u05D2", // pure Hebrew
		"\u0628\u0629\u062A", // pure Arabic
	}
	for _, s := range pure {
		if DetectMixedScript(s) {
			t.Errorf("expected pure: %q", s)
		}
	}
}

// TestSanitizeWorkflowUnicode_WarnsOnMixedScriptName -
// coverage for the DetectMixedScript branch inside
// SanitizeWorkflowUnicode. The call is observational (slog.Warn), so
// the test only needs to exercise the branch; we assert no error is
// returned and the name survives verbatim.
func TestSanitizeWorkflowUnicode_WarnsOnMixedScriptName(t *testing.T) {
	wf := &Workflow{
		// Latin 'p' + Cyrillic 'а' (U+0430) + Latin suffix.
		Name:  "p\u0430ypal",
		Steps: []Step{{ID: "a", Instructions: "ok"}},
	}
	if err := SanitizeWorkflowUnicode(wf); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if wf.Name != "p\u0430ypal" {
		t.Fatalf("name mutated: %q", wf.Name)
	}
}
