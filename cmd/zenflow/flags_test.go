package main

import (
	"strings"
	"testing"
)

// TestParseFlags_KeyEqualsValueForm covers the first-pass
// normaliser that splits `--key=value` into separate tokens.
func TestParseFlags_KeyEqualsValueForm(t *testing.T) {
	f, err := parseFlags([]string{"--model=gpt-5", "--max-concurrency=3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5", f.model)
	}
	if f.maxConcurrency != 3 {
		t.Errorf("maxConcurrency = %d, want 3", f.maxConcurrency)
	}
}

// TestParseFlags_PosixDoubleDashTerminator covers the new --
// terminator branch - anything after `--` should be rejected.
func TestParseFlags_PosixDoubleDashTerminator(t *testing.T) {
	_, err := parseFlags([]string{"--model", "x", "--", "extra"})
	if err == nil {
		t.Fatal("expected error for positional after --")
	}
	if !strings.Contains(err.Error(), "after `--`") {
		t.Errorf("err = %q, want mention of '-- terminator'", err)
	}
}

// TestParseFlags_RejectsNegativeMaxConcurrency covers the new range
// guard added to --max-concurrency.
func TestParseFlags_RejectsNegativeMaxConcurrency(t *testing.T) {
	_, err := parseFlags([]string{"--max-concurrency", "-5"})
	if err == nil {
		t.Fatal("expected error for negative max-concurrency")
	}
	if !strings.Contains(err.Error(), "must be >= 1") {
		t.Errorf("err = %q, want range message", err)
	}
}

// TestParseFlags_RejectsNegativeMaxRetries covers the new range guard
// added to --max-retries.
func TestParseFlags_RejectsNegativeMaxRetries(t *testing.T) {
	_, err := parseFlags([]string{"--max-retries", "-3"})
	if err == nil {
		t.Fatal("expected error for negative max-retries")
	}
	if !strings.Contains(err.Error(), "must be >= 0") {
		t.Errorf("err = %q, want range message", err)
	}
}

// TestParseFlags_BarePositionalRejectedByDefault covers the new
// "unexpected positional argument" friendly message in the switch
// default branch (was: misleading "unknown flag: <bareword>").
func TestParseFlags_BarePositionalRejectedByDefault(t *testing.T) {
	// "extra" doesn't start with `-` so it lands in the default branch
	// - the new code emits "unexpected positional argument" instead of
	// "unknown flag: extra".
	_, err := parseFlags([]string{"--verbose", "extra"})
	if err == nil {
		t.Fatal("expected error for bare positional")
	}
	if !strings.Contains(err.Error(), "unexpected positional argument") {
		t.Errorf("err = %q, want 'unexpected positional argument'", err)
	}
}

// TestArgsContainHelp covers the helper used by every subcommand
// dispatcher to detect `--help`/`-h`/`help` anywhere in the args.
func TestArgsContainHelp(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"empty", []string{}, false},
		{"plain args", []string{"file.yaml", "--model", "x"}, false},
		{"long help", []string{"file.yaml", "--help"}, true},
		{"short help", []string{"-h", "file.yaml"}, true},
		{"bare help", []string{"file.yaml", "help"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := argsContainHelp(tc.args); got != tc.want {
				t.Errorf("argsContainHelp(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestSplitProviderModel_TrimsWhitespace covers the hardening
// for `--model " google/gemini-2.5-flash"` (silently misrouted to
// auto-detection before the trim was added). Before the fix
// providerName would be " google" which doesn't match any case in
// resolveModel and falls through to autoModelFromModelName, picking
// Azure when AZURE_OPENAI_API_KEY is set.
func TestSplitProviderModel_TrimsWhitespace(t *testing.T) {
	cases := []struct {
		input        string
		wantProvider string
		wantModel    string
	}{
		{"  google/gemini-2.5-flash  ", "google", "gemini-2.5-flash"},
		{" google/gemini-2.5-flash", "google", "gemini-2.5-flash"},
		{"google/gemini-2.5-flash ", "google", "gemini-2.5-flash"},
		{"\tgoogle/gemini-2.5-flash\n", "google", "gemini-2.5-flash"},
		{"   bare-model   ", "", "bare-model"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			gotProvider, gotModel := splitProviderModel(tc.input)
			if gotProvider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", gotProvider, tc.wantProvider)
			}
			if gotModel != tc.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tc.wantModel)
			}
		})
	}
}

// TestResolveProvider_RejectsEmptyModelID covers the 
// `provider/` empty-model-id guard. Before the fix, `--model "google/"`
// produced a non-nil model with empty model ID, HasLLM returned true,
// and the request 404ed at HTTP. Now the empty model ID short-circuits
// resolveProvider so HasLLM reports false and the CLI prints
// "no LLM model configured".
func TestResolveProvider_RejectsEmptyModelID(t *testing.T) {
	cases := []string{
		"google/",
		"bedrock/",
		"azure/",
		"vertex/",
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			llm, modelID := resolveProvider(m)
			if llm != nil {
				t.Errorf("llm = %v, want nil for empty model id", llm)
			}
			if modelID != "" {
				t.Errorf("modelID = %q, want empty string", modelID)
			}
		})
	}
}

// TestResolveProvider_VertexPrefix covers the explicit `vertex/`
// prefix. Before the fix `vertex/<model>` fell through to
// autoModelFromModelName which silently routed Gemini-named models to
// the Google Gemini API (when GEMINI_API_KEY was set), overriding user
// intent. The vertex provider is constructed here without ADC checks
// (those would race in unit tests) - the constructor returns a non-nil
// provider.LanguageModel that fails at first request if ADC is missing.
func TestResolveProvider_VertexPrefix(t *testing.T) {
	llm, modelID := resolveProvider("vertex/gemini-2.0-flash")
	if llm == nil {
		t.Fatal("vertex/<model> returned nil; vertex prefix not wired")
	}
	if modelID != "gemini-2.0-flash" {
		t.Errorf("modelID = %q, want gemini-2.0-flash", modelID)
	}
}

// TestResolveProvider_VertexAnthropicPrefix covers the 
// `vertex-anthropic/` prefix for Claude on Vertex.
func TestResolveProvider_VertexAnthropicPrefix(t *testing.T) {
	llm, modelID := resolveProvider("vertex-anthropic/claude-sonnet-4@20250514")
	if llm == nil {
		t.Fatal("vertex-anthropic/<model> returned nil; prefix not wired")
	}
	if modelID != "claude-sonnet-4@20250514" {
		t.Errorf("modelID = %q, want claude-sonnet-4@20250514", modelID)
	}
}

// TestCmd_HelpFlagDispatch verifies every subcommand's --help branch
// prints usage to stdout and exits cleanly without invoking
// the underlying handler.
func TestCmd_HelpFlagDispatch(t *testing.T) {
	subcommands := []struct {
		name string
		args []string
		fn   func()
	}{
		{"validate", []string{"zenflow", "validate", "--help"}, cmdValidate},
		{"plan", []string{"zenflow", "plan", "--help"}, cmdPlan},
		{"flow", []string{"zenflow", "flow", "--help"}, cmdFlow},
		{"goal", []string{"zenflow", "goal", "--help"}, cmdGoal},
		{"agent", []string{"zenflow", "agent", "--help"}, cmdAgent},
	}
	for _, tc := range subcommands {
		t.Run(tc.name, func(t *testing.T) {
			env, cleanup := setupTest(t, tc.args...)
			defer cleanup()
			tc.fn()
			if env.exitCode != -1 {
				t.Errorf("exit = %d, want exit not called", env.exitCode)
			}
			if !strings.Contains(env.stdout.String(), "Usage: zenflow") {
				t.Errorf("stdout missing usage; got %q", env.stdout.String())
			}
		})
	}
}
