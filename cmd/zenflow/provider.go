package main

// provider.go - --model flag → provider.LanguageModel resolution.
// Format: "provider/model" (e.g., "google/gemini-2.5-flash",
// "bedrock/anthropic.claude-sonnet-4-6"). Bare model names without a
// provider prefix are also supported via autoModelFromModelName, which
// guesses the provider from the model name + which API-key env var is
// set. Env vars considered: GEMINI_API_KEY,
// GOOGLE_GENERATIVE_AI_API_KEY, AWS_ACCESS_KEY_ID,
// AZURE_OPENAI_API_KEY, ZENFLOW_MODEL.

import (
	"os"
	"strings"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/azure"
	"github.com/zendev-sh/goai/provider/bedrock"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/vertex"
)

// resolveProvider creates a LanguageModel from the --model flag value.
// Format: "provider/model" (e.g., "google/gemini-2.5-flash", "bedrock/anthropic.claude-sonnet-4-6").
// Bare model names without provider prefix are also supported for convenience.
// Env vars: GEMINI_API_KEY, AWS_ACCESS_KEY_ID, AZURE_OPENAI_API_KEY, ZENFLOW_MODEL.
func resolveProvider(modelFlag string) (provider.LanguageModel, string) {
	// Check ZENFLOW_MODEL env var as fallback.
	if modelFlag == "" {
		modelFlag = os.Getenv("ZENFLOW_MODEL")
	}
	if modelFlag == "" {
		return nil, ""
	}

	// Split provider/model format.
	providerName, modelID := splitProviderModel(modelFlag)

	// Reject empty model ID after a provider prefix (e.g. "google/").
	// Without this, the empty modelID propagated through
	// google.Chat("")/bedrock.Chat("")/etc. and surfaced as a confusing
	// HTTP 404 at first invocation. HasLLM also returned true because
	// the constructors return non-nil even with an empty model. Fail-fast
	// at flag-resolution time and return nil so HasLLM reports false.
	if providerName != "" && modelID == "" {
		return nil, ""
	}

	// reject model IDs with a leading colon (e.g. `:gpt-5`).
	// A leading colon is never a valid model name and indicates a
	// malformed flag value (e.g. forgotten provider prefix or copy-paste
	// from a YAML key). Fail-fast here rather than letting the malformed
	// ID propagate to a provider constructor that may silently accept it.
	if strings.HasPrefix(modelID, ":") {
		return nil, ""
	}

	model := resolveModel(providerName, modelID)
	return model, modelID
}

// resolveModel creates a LanguageModel based on an explicit provider name.
// If providerName is empty (bare model), it delegates to autoModelFromModelName.
func resolveModel(providerName, modelID string) provider.LanguageModel {
	switch providerName {
	case "google":
		return google.Chat(modelID)
	case "bedrock":
		return bedrock.Chat(modelID)
	case "azure":
 // Use goai's default routing - newer v1 GA URL path
 // (`/openai/v1{path}`) which is Microsoft's recommended
 // forward direction. goai v0.7.4+ correctly omits the
 // `?api-version=` query parameter on this path per Azure's
 // v1 GA spec, so spec-strict resources accept it.
 // Internal goai routing still handles the per-model-family
 // distinctions (Claude → Anthropic protocol; non-OpenAI →
 // AI Services endpoint; OpenAI → v1 GA path; codex/pro →
 // cognitiveservices Responses API when WithDeploymentBasedURLs
 // is set, opt in explicitly via the azure-deployment/ prefix).
 // See https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle
		return azure.Chat(modelID)
	case "azure-deployment":
 // Explicit opt-in to the legacy deployment-based URL pattern
 // (`/openai/deployments/{model}/...?api-version=...`). Use this
 // when (a) your Azure resource doesn't yet expose v1 GA, (b) you
 // need a specific dated api-version, or (c) you're talking to a
 // model variant that requires the cognitiveservices Responses
 // API (codex/pro family). goai routes codex/pro via the
 // Responses API automatically when this flag is set.
		return azure.Chat(modelID, azure.WithDeploymentBasedURLs(true))
	case "vertex":
 // Explicit Google Vertex AI prefix. Without this case vertex/<model>
 // fell through to autoModelFromModelName which silently routed
 // Gemini-named models to the Google Gemini API when GEMINI_API_KEY
 // was set, overriding user intent. Vertex requires Application Default
 // Credentials (gcloud auth login && gcloud auth application-default
 // login); see the goai vertex package docs for region / project
 // configuration.
		return vertex.Chat(modelID)
	case "vertex-anthropic":
 // Vertex's Anthropic offering uses a different protocol and endpoint
 // shape; explicit prefix so callers don't have to guess. Same ADC
 // requirement as vertex/.
		return vertex.AnthropicChat(modelID)
	default:
 // No provider prefix - auto-detect from model name and env vars.
		return autoModelFromModelName(modelID)
	}
}

// splitProviderModel splits "provider/model" into (provider, model).
// If no "/" is present, returns ("", fullString) for auto-detection.
// Leading and trailing whitespace are trimmed BEFORE the split. Without
// trimming, --model " google/gemini-2.5-flash" would produce
// provider=" google" which doesn't match any case in resolveModel and
// silently falls through to autoModelFromModelName, routing the request
// to Azure when AZURE_OPENAI_API_KEY is set (overriding user intent).
// Both halves of the split are also trimmed so trailing whitespace in
// the model ID part doesn't survive.
func splitProviderModel(s string) (string, string) {
	s = strings.TrimSpace(s)
	for i, c := range s {
		if c == '/' {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
		}
	}
	return "", s
}

// isBedrockCrossRegionPattern returns true for Bedrock cross-region
// inference profile names. Pattern: <vendor>.<model> where vendor is
// a known AWS Bedrock vendor (anthropic, amazon, meta, mistral,
// cohere, ai21, stability, minimax, deepseek-via-bedrock).
// replaces the prior naive `strings.Contains(modelName, ".")`
// heuristic that wrongly matched Azure model IDs with version dots
// (e.g. "DeepSeek-V3.2", "Llama-3.1-70B"). The narrower vendor-prefix
// check correctly distinguishes "anthropic.claude-sonnet-4-6" (Bedrock)
// from "DeepSeek-V3.2" (Azure AI Services).
func isBedrockCrossRegionPattern(modelName string) bool {
	bedrockVendors := []string{
		"anthropic.", "amazon.", "meta.", "mistral.", "cohere.",
		"ai21.", "stability.", "minimax.", "deepseek.", "qwen.",
		"writer.", "luma.", "openai.",
	}
	lower := strings.ToLower(modelName)
	for _, prefix := range bedrockVendors {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// autoModelFromModelName creates a LanguageModel by guessing the provider from model name.
// Used when --model has no provider/ prefix (bare model name).
func autoModelFromModelName(modelName string) provider.LanguageModel {
	hasGemini := os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY") != ""
	hasBedrock := os.Getenv("AWS_ACCESS_KEY_ID") != ""
	hasAzure := os.Getenv("AZURE_OPENAI_API_KEY") != ""

	switch {
	case len(modelName) >= 7 && modelName[:7] == "gemini-":
		if hasGemini {
			return google.Chat(modelName)
		}
	case isBedrockCrossRegionPattern(modelName):
 // Bedrock cross-region: "anthropic.claude-*", "minimax.*",
 // "amazon.nova-*", etc. Vendor prefix BEFORE the dot, model
 // name AFTER. - narrowed from `strings.Contains(".")`
 // because that caught Azure model IDs with version dots
 // (e.g. "DeepSeek-V3.2", "Llama-3.1-70B") and routed them
 // to Bedrock by mistake → "model identifier is invalid".
		if hasBedrock {
			return bedrock.Chat(modelName)
		}
	case len(modelName) >= 4 && modelName[:4] == "gpt-":
		if hasAzure {
			return azure.Chat(modelName, azure.WithDeploymentBasedURLs(true))
		}
	case len(modelName) >= 7 && modelName[:7] == "claude-":
		if hasAzure {
			return azure.Chat(modelName)
		}
	default:
		if hasAzure {
			return azure.Chat(modelName)
		}
	}

	// Fallback: try any available provider.
	if hasGemini {
		return google.Chat(modelName)
	}
	if hasAzure {
		return azure.Chat(modelName)
	}
	if hasBedrock {
		return bedrock.Chat(modelName)
	}
	return nil
}
