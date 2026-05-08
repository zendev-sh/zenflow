package main

// thinking.go - --thinking <level> → ProviderOptions map.
// The map carries every provider's native reasoning key so the same
// flag works across Bedrock (anthropic + DeepSeek r1), Anthropic
// direct, Google Gemini, and OpenAI/Azure Responses (gpt-5 / codex /
// pro). Each provider reads only the keys it understands; unknown
// keys are ignored, so a single map is forward-compatible with
// future providers as long as they continue the "ProviderOptions
// map[string]any" pattern.

// thinkingProviderOptions translates --thinking <level> into a single
// ProviderOptions map containing every provider's native reasoning key.
// Each provider reads only the keys it understands; unknown keys are
// ignored, so a single map covers all currently wired providers
// (bedrock, anthropic, google, openai/azure-responses).
// Returns nil for "off" or empty level.
func thinkingProviderOptions(level string) map[string]any {
	if level == "" || level == "off" {
		return nil
	}
	var budget int
	switch level {
	case "low":
		budget = 1024
	case "medium":
		budget = 4096
	case "high":
		budget = 16384
	default:
		return nil
	}
	return map[string]any{
 // Bedrock: anthropic models read {type, budgetTokens}; non-anthropic
 // (DeepSeek r1) read {maxReasoningEffort}.
		"reasoningConfig": map[string]any{
			"type":               "enabled",
			"budgetTokens":       budget,
			"maxReasoningEffort": level,
		},
 // Anthropic direct: ProviderOptions["thinking"] = {type, budgetTokens}.
		"thinking": map[string]any{
			"type":         "enabled",
			"budgetTokens": budget,
		},
 // Google Gemini: ProviderOptions["thinkingConfig"] with includeThoughts
 // + thinkingLevel (gemini-3) or thinkingBudget (gemini-2.5).
		"thinkingConfig": map[string]any{
			"includeThoughts": true,
			"thinkingLevel":   level,
			"thinkingBudget":  budget,
		},
 // OpenAI Responses (used by Azure GPT-5/codex too): reasoning_effort
 // is a top-level option, reasoning_summary requests visible summaries.
		"reasoning_effort":  level,
		"reasoning_summary": "auto",
	}
}
