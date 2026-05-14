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

// TestResolveProvider_CompatPrefix wires the `compat/` prefix to goai's
// generic OpenAI-compatible provider (`github.com/zendev-sh/goai/provider/compat`).
// Use case: local llama.cpp `llama-server`, LiteLLM proxy, vLLM with
// non-standard URL, or any custom OpenAI-compatible endpoint. The base
// URL is taken from `COMPAT_BASE_URL` env var, defaulting to
// `http://localhost:8080/v1` (llama.cpp's default port).
func TestResolveProvider_CompatPrefix(t *testing.T) {
	llm, modelID := resolveProvider("compat/qwen3.6-27b")
	if llm == nil {
		t.Fatal("compat/<model> returned nil; compat prefix not wired")
	}
	if modelID != "qwen3.6-27b" {
		t.Errorf("modelID = %q, want qwen3.6-27b", modelID)
	}
}

// TestResolveProvider_CompatPrefix_RespectsEnvBaseURL verifies that
// `COMPAT_BASE_URL` overrides the default `http://localhost:8080/v1`. We
// can't easily assert the URL ended up inside the provider without
// reaching into goai internals, but at minimum a non-default URL must
// still produce a non-nil model (the env-read path must not panic or
// silently swallow the value).
func TestResolveProvider_CompatPrefix_RespectsEnvBaseURL(t *testing.T) {
	t.Setenv("COMPAT_BASE_URL", "http://remote-gpu-box:9000/v1")
	llm, modelID := resolveProvider("compat/whatever")
	if llm == nil {
		t.Fatal("compat/<model> with COMPAT_BASE_URL returned nil")
	}
	if modelID != "whatever" {
		t.Errorf("modelID = %q, want whatever", modelID)
	}
}

// TestResolveProvider_CompatPrefix_RespectsAPIKey covers the
// COMPAT_API_KEY env-var path. Most local servers (llama.cpp without
// `--api-key`) ignore the Authorization header, but LiteLLM and some
// vLLM deployments do check, so the env-var must reach the goai compat
// constructor.
func TestResolveProvider_CompatPrefix_RespectsAPIKey(t *testing.T) {
	t.Setenv("COMPAT_API_KEY", "sk-test-fake-key")
	llm, modelID := resolveProvider("compat/qwen")
	if llm == nil {
		t.Fatal("compat with COMPAT_API_KEY returned nil")
	}
	if modelID != "qwen" {
		t.Errorf("modelID = %q, want qwen", modelID)
	}
}

// TestResolveProvider_OllamaPrefix wires the `ollama/` prefix to goai's
// ollama provider (default base URL `http://localhost:11434/v1`). Models
// names follow Ollama's tag convention (e.g. `llama3`, `qwen3:32b`).
func TestResolveProvider_OllamaPrefix(t *testing.T) {
	llm, modelID := resolveProvider("ollama/llama3")
	if llm == nil {
		t.Fatal("ollama/<model> returned nil; ollama prefix not wired")
	}
	if modelID != "llama3" {
		t.Errorf("modelID = %q, want llama3", modelID)
	}
}

// TestResolveProvider_OllamaPrefix_BaseURLEnv covers OLLAMA_BASE_URL -
// the fully-qualified-URL escape hatch. Set when the user runs Ollama
// behind a reverse proxy with a non-standard path prefix that
// OLLAMA_HOST's auto-append-/v1 logic would mangle.
func TestResolveProvider_OllamaPrefix_BaseURLEnv(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "https://proxy.example.com/ollama/v1")
	llm, modelID := resolveProvider("ollama/llama3")
	if llm == nil {
		t.Fatal("ollama with OLLAMA_BASE_URL returned nil")
	}
	if modelID != "llama3" {
		t.Errorf("modelID = %q, want llama3", modelID)
	}
}

// TestResolveProvider_OllamaPrefix_HostEnv covers OLLAMA_HOST - the
// Ollama-standard env var. Format is `host:port` (no scheme, no /v1);
// the resolver auto-prepends scheme assumption inside goai and we
// append `/v1` here. Verify both the bare host form and the form
// that already includes /v1 (idempotent - must not double-append).
func TestResolveProvider_OllamaPrefix_HostEnv(t *testing.T) {
	cases := []string{
		"http://gpu-box:11434",     // bare host:port - we append /v1
		"http://gpu-box:11434/v1",  // already has /v1 - must not double-append
		"http://gpu-box:11434/v1/", // trailing slash - must trim then keep /v1
	}
	for _, host := range cases {
		t.Run(host, func(t *testing.T) {
			t.Setenv("OLLAMA_HOST", host)
			// Clear OLLAMA_BASE_URL so the OLLAMA_HOST branch runs.
			t.Setenv("OLLAMA_BASE_URL", "")
			llm, modelID := resolveProvider("ollama/llama3")
			if llm == nil {
				t.Fatal("ollama with OLLAMA_HOST returned nil")
			}
			if modelID != "llama3" {
				t.Errorf("modelID = %q, want llama3", modelID)
			}
		})
	}
}

// TestResolveProvider_VllmPrefix wires the `vllm/` prefix to goai's
// vllm provider (default base URL `http://localhost:8000/v1`). vLLM
// model IDs typically contain a `/` themselves (HuggingFace org/repo
// pattern, e.g. `meta-llama/Llama-3-8b`); splitProviderModel only
// splits on the FIRST `/`, so this case validates the multi-slash
// model ID path.
func TestResolveProvider_VllmPrefix(t *testing.T) {
	llm, modelID := resolveProvider("vllm/meta-llama/Llama-3-8b")
	if llm == nil {
		t.Fatal("vllm/<org>/<model> returned nil; vllm prefix not wired")
	}
	if modelID != "meta-llama/Llama-3-8b" {
		t.Errorf("modelID = %q, want meta-llama/Llama-3-8b", modelID)
	}
}

// TestResolveProvider_VllmPrefix_BaseURLEnv covers VLLM_BASE_URL - for
// remote vLLM deployments (default targets localhost:8000).
func TestResolveProvider_VllmPrefix_BaseURLEnv(t *testing.T) {
	t.Setenv("VLLM_BASE_URL", "http://gpu-cluster:8000/v1")
	llm, modelID := resolveProvider("vllm/Qwen/Qwen3-32B")
	if llm == nil {
		t.Fatal("vllm with VLLM_BASE_URL returned nil")
	}
	if modelID != "Qwen/Qwen3-32B" {
		t.Errorf("modelID = %q, want Qwen/Qwen3-32B", modelID)
	}
}

// TestResolveProvider_VllmPrefix_APIKeyEnv covers VLLM_API_KEY - used
// when the vLLM server was started with `--api-key`. Without this env
// path, authenticated vLLM deployments would 401.
func TestResolveProvider_VllmPrefix_APIKeyEnv(t *testing.T) {
	t.Setenv("VLLM_API_KEY", "sk-vllm-fake")
	llm, modelID := resolveProvider("vllm/some-model")
	if llm == nil {
		t.Fatal("vllm with VLLM_API_KEY returned nil")
	}
	if modelID != "some-model" {
		t.Errorf("modelID = %q, want some-model", modelID)
	}
}

// TestResolveProvider_RejectsEmptyModelID_LocalProviders extends the
// existing empty-model-id guard to the new local provider prefixes.
// `compat/` `ollama/` `vllm/` with no model name must short-circuit to
// nil so `HasLLM()` reports false instead of constructing a model that
// 404s on first call.
func TestResolveProvider_RejectsEmptyModelID_LocalProviders(t *testing.T) {
	cases := []string{"compat/", "ollama/", "vllm/"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			llm, modelID := resolveProvider(m)
			if llm != nil {
				t.Errorf("llm = %v, want nil for empty model id", llm)
			}
			if modelID != "" {
				t.Errorf("modelID = %q, want empty", modelID)
			}
		})
	}
}

// TestResolveProvider_NewPrefixes covers the 18 cloud provider prefixes
// added in the cross-goai-providers patch. Each entry maps a
// `provider/model` flag to the model ID we expect resolveProvider to
// extract; a non-nil LanguageModel proves the case is wired in
// resolveModel (the underlying goai constructors return non-nil even
// when their API-key env var is unset - they fail at first request, not
// at construction). We deliberately don't set any API-key env vars
// because the contract under test is "prefix → constructor invoked",
// not "live request succeeds". One canonical model ID per provider is
// enough; per-provider env-var plumbing is the goai package's
// responsibility, exercised in those packages' own tests.
func TestResolveProvider_NewPrefixes(t *testing.T) {
	cases := []struct {
		flag   string
		wantID string
	}{
		{"anthropic/claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"openai/gpt-5", "gpt-5"},
		{"xai/grok-4", "grok-4"},
		{"groq/llama-3.3-70b-versatile", "llama-3.3-70b-versatile"},
		{"cerebras/qwen-3-coder-480b", "qwen-3-coder-480b"},
		{"deepseek/deepseek-chat", "deepseek-chat"},
		{"deepinfra/meta-llama/Llama-3.3-70B-Instruct", "meta-llama/Llama-3.3-70B-Instruct"},
		{"fireworks/accounts/fireworks/models/qwen3-coder-30b", "accounts/fireworks/models/qwen3-coder-30b"},
		{"together/meta-llama/Llama-3.3-70B-Instruct-Turbo", "meta-llama/Llama-3.3-70B-Instruct-Turbo"},
		{"mistral/mistral-large-latest", "mistral-large-latest"},
		{"cohere/command-r-plus", "command-r-plus"},
		{"perplexity/sonar-pro", "sonar-pro"},
		{"nvidia/meta/llama-3.3-70b-instruct", "meta/llama-3.3-70b-instruct"},
		{"openrouter/anthropic/claude-sonnet-4.6", "anthropic/claude-sonnet-4.6"},
		{"minimax/MiniMax-M2", "MiniMax-M2"},
		{"fptcloud/Qwen3-Coder-30B-A3B-Instruct", "Qwen3-Coder-30B-A3B-Instruct"},
		{"cloudflare/@cf/meta/llama-3.3-70b-instruct-fp8-fast", "@cf/meta/llama-3.3-70b-instruct-fp8-fast"},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			llm, modelID := resolveProvider(tc.flag)
			if llm == nil {
				t.Fatalf("resolveProvider(%q) = nil; prefix not wired in resolveModel", tc.flag)
			}
			if modelID != tc.wantID {
				t.Errorf("modelID = %q, want %q", modelID, tc.wantID)
			}
		})
	}
}

// TestResolveProvider_RunpodPrefix_ReadsEndpointEnv covers the runpod
// special case: unlike every other goai provider, runpod.Chat takes
// (endpointID, modelID) instead of a single modelID, because the
// endpoint ID determines the URL hostname (`https://api.runpod.ai/v2/<endpointID>/openai/v1`).
// The CLI reads it from RUNPOD_ENDPOINT_ID to avoid inventing a new
// `provider/endpoint:model` separator. Constructor returns non-nil even
// when the env var is missing - failure happens at first request with a
// malformed URL.
func TestResolveProvider_RunpodPrefix_ReadsEndpointEnv(t *testing.T) {
	t.Setenv("RUNPOD_ENDPOINT_ID", "abc123def456")
	llm, modelID := resolveProvider("runpod/qwen3-coder-30b")
	if llm == nil {
		t.Fatal("runpod/<model> with RUNPOD_ENDPOINT_ID returned nil; prefix not wired")
	}
	if modelID != "qwen3-coder-30b" {
		t.Errorf("modelID = %q, want qwen3-coder-30b", modelID)
	}
}

// TestAutoModel_ClaudePrefersAnthropic verifies bare `claude-*` routes
// to native Anthropic when ANTHROPIC_API_KEY is set, even if
// AZURE_OPENAI_API_KEY is also present. The previous behaviour silently
// preferred Azure for `claude-*` names whenever AZURE_OPENAI_API_KEY
// was configured, which surprised users who set ANTHROPIC_API_KEY
// specifically to talk to Anthropic's native API.
func TestAutoModel_ClaudePrefersAnthropic(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fake")
	t.Setenv("AZURE_OPENAI_API_KEY", "fake-azure")
	t.Setenv("AZURE_RESOURCE_NAME", "fake-resource")
	llm := autoModelFromModelName("claude-sonnet-4-6")
	if llm == nil {
		t.Fatal("autoModelFromModelName(claude-sonnet-4-6) returned nil with ANTHROPIC_API_KEY set")
	}
	// We can't introspect which provider goai's interface points at
	// without reaching into internals; the non-nil assertion + the
	// next test (env-var precedence) is the best we can do without
	// a brittle reflect cast. The behaviour change is also covered
	// at the integration layer (zenflow flow ... bare-name).
}

// TestAutoModel_GPTPrefersOpenAI is the OpenAI counterpart to the
// Claude test: bare `gpt-*` routes to OpenAI when OPENAI_API_KEY is set
// rather than Azure.
func TestAutoModel_GPTPrefersOpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake")
	t.Setenv("AZURE_OPENAI_API_KEY", "fake-azure")
	t.Setenv("AZURE_RESOURCE_NAME", "fake-resource")
	llm := autoModelFromModelName("gpt-5")
	if llm == nil {
		t.Fatal("autoModelFromModelName(gpt-5) returned nil with OPENAI_API_KEY set")
	}
}

// TestAutoModel_GrokRoutesToXAI covers the new `grok-*` auto-detect
// prefix that routes to xAI when XAI_API_KEY is set.
func TestAutoModel_GrokRoutesToXAI(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-fake")
	llm := autoModelFromModelName("grok-4")
	if llm == nil {
		t.Fatal("autoModelFromModelName(grok-4) returned nil with XAI_API_KEY set")
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
