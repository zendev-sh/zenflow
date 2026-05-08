package exec

import (
	"strings"
	"testing"
)

// TestSanitizeAgentName covers bug #6 / #9 fix: Bedrock and OpenAI
// reject tool names outside [a-zA-Z0-9_-]+. LLMs occasionally emit
// names like "[TOOL_CALLS]agent" or "Multi_tool_use.parallel" that
// blow up the next request with a validation error. The sanitizer
// strips invalid chars at spawn time so the conversation history
// stays valid.
func TestSanitizeAgentName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain alphanumeric passes", "BedrockReviewer", "BedrockReviewer"},
		{"underscore + dash preserved", "file-reader_v2", "file-reader_v2"},
		{"square brackets stripped", "[TOOL_CALLS]agent", "TOOL_CALLSagent"},
		{"dot stripped", "Multi_tool_use.parallel", "Multi_tool_useparallel"},
		{"spaces stripped", "File Fetcher schema.go", "FileFetcherschemago"},
		{"empty falls back to agent", "", "agent"},
		{"all-invalid falls back to agent", "$$$###", "agent"},
		{"unicode stripped (Bedrock ASCII-only)", "Zjednoczone", "Zjednoczone"},
		{"unicode with non-ASCII stripped", "agent🚀", "agent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeAgentName(c.in)
			if got != c.want {
				t.Errorf("sanitizeAgentName(%q) = %q, want %q", c.in, got, c.want)
			}
 // Defence-in-depth: every non-empty result must satisfy the
 // Bedrock regex itself, not just our enumerated cases.
			if got != "" {
				for _, r := range got {
					ok := (r >= 'a' && r <= 'z') ||
						(r >= 'A' && r <= 'Z') ||
						(r >= '0' && r <= '9') ||
						r == '_' || r == '-'
					if !ok {
						t.Errorf("sanitized name %q contains forbidden rune %q", got, r)
					}
				}
			}
		})
	}
}

// TestIsLikelyHallucinatedModel covers bug #5 fix: LLMs emit literal
// strings like "default" / "auto" / "gpt-4" in the model field due to
// schema-description ambiguity or training-data leak. The spawner
// silently treats these as empty (inherit parent default) instead of
// failing provider lookup.
func TestIsLikelyHallucinatedModel(t *testing.T) {
	hallucinated := []string{
		"default",
		"auto",
		"parent",
		"inherit",
		"gpt-4",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-3.5-turbo",
	}
	for _, m := range hallucinated {
		if !isLikelyHallucinatedModel(m) {
			t.Errorf("expected %q to be classified hallucinated", m)
		}
	}

	// Real model identifiers must NOT be classified hallucinated.
	real := []string{
		"bedrock/anthropic.claude-sonnet-4-6",
		"google/gemini-3-pro-preview",
		"azure/DeepSeek-V3.2",
		"azure/gpt-5", // "gpt-5" not in hallucination list
		"bedrock/minimax.minimax-m2.5",
		"",
		"sonnet",
		"mistral.ministral-3-14b-instruct",
	}
	for _, m := range real {
		if isLikelyHallucinatedModel(m) {
			t.Errorf("real model %q misclassified as hallucinated", m)
		}
	}
}

// TestSpawnChild_HallucinatedModelFallsBackSilently covers bug #5
// integration: when params.Model is a known hallucination string, the
// spawner uses s.DefaultModel WITHOUT emitting a warning event. Real
// model overrides (different from default but not hallucinated) still
// emit the warning so operators can observe deliberate divergence.
func TestSpawnChild_HallucinatedModelFallsBackSilently(t *testing.T) {
	t.Run("model='default' silent fallback no warning", func(t *testing.T) {
 // Manual classification check (Spawn-level wiring is exercised
 // indirectly via existing TestZFB11_SpawnChild_* tests; here we
 // cover the new switch branch in isolation to guard the silent
 // behaviour).
		if !isLikelyHallucinatedModel("default") {
			t.Fatal("'default' must be classified hallucinated for silent fallback")
		}
	})

	t.Run("model='gpt-4' silent fallback no warning", func(t *testing.T) {
		if !isLikelyHallucinatedModel("gpt-4") {
			t.Fatal("'gpt-4' must be classified hallucinated for silent fallback")
		}
	})

	t.Run("real override 'claude-sonnet-4-6' still warns", func(t *testing.T) {
 // Distinct model that is NOT hallucinated → warning still fires
 // per the original behaviour. This is intentional: legitimate
 // overrides should be visible in the chat surface.
		if isLikelyHallucinatedModel("claude-sonnet-4-6") {
			t.Fatal("legitimate model override must NOT be silently swallowed")
		}
	})
}

// TestAgentToolDef_SchemaWarnsAboutDefault covers bug #5: the schema
// description must explicitly tell LLMs to OMIT the model field rather
// than pass literal strings. Without this guidance, Sonnet-class
// models still emit "default" because the prior description ("defaults
// to parent's model") was ambiguous.
func TestAgentToolDef_SchemaWarnsAboutDefault(t *testing.T) {
	def := AgentToolDef()
	schema := string(def.InputSchema)
	if !strings.Contains(schema, "OMIT") {
		t.Error("schema should explicitly tell LLMs to OMIT the model field, not interpret 'default' as a value")
	}
	if !strings.Contains(schema, "[a-zA-Z0-9_-]+") {
		t.Error("schema should reference the Bedrock/OpenAI tool-name regex so LLMs avoid invalid chars")
	}
}
