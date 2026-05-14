package main

// provider.go - --model flag → provider.LanguageModel resolution.
// Format: "provider/model" (e.g., "google/gemini-2.5-flash",
// "bedrock/anthropic.claude-sonnet-4-6"). Bare model names without a
// provider prefix are also supported via autoModelFromModelName, which
// guesses the provider from the model name + which API-key env var is
// set.
//
// Supported provider prefixes: anthropic, azure, azure-deployment,
// bedrock, cerebras, cloudflare, cohere, compat, deepinfra, deepseek,
// fireworks, fptcloud, google, groq, minimax, mistral, nvidia, ollama,
// openai, openrouter, perplexity, runpod, together, vertex,
// vertex-anthropic, vllm, xai.
//
// Each provider's API key (and other config) is read from its standard
// env var by the underlying goai constructor: ANTHROPIC_API_KEY,
// CEREBRAS_API_KEY, CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID,
// COHERE_API_KEY, DEEPINFRA_API_KEY, DEEPSEEK_API_KEY,
// FIREWORKS_API_KEY, FPT_API_KEY (+ FPT_REGION), GEMINI_API_KEY,
// GOOGLE_GENERATIVE_AI_API_KEY, GROQ_API_KEY, MINIMAX_API_KEY,
// MISTRAL_API_KEY, NVIDIA_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY,
// PERPLEXITY_API_KEY, RUNPOD_API_KEY + RUNPOD_ENDPOINT_ID,
// TOGETHER_AI_API_KEY (or TOGETHER_API_KEY), AWS_ACCESS_KEY_ID,
// AZURE_OPENAI_API_KEY, XAI_API_KEY, ZENFLOW_MODEL.

import (
	"os"
	"strings"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/anthropic"
	"github.com/zendev-sh/goai/provider/azure"
	"github.com/zendev-sh/goai/provider/bedrock"
	"github.com/zendev-sh/goai/provider/cerebras"
	"github.com/zendev-sh/goai/provider/cloudflare"
	"github.com/zendev-sh/goai/provider/cohere"
	"github.com/zendev-sh/goai/provider/compat"
	"github.com/zendev-sh/goai/provider/deepinfra"
	"github.com/zendev-sh/goai/provider/deepseek"
	"github.com/zendev-sh/goai/provider/fireworks"
	"github.com/zendev-sh/goai/provider/fptcloud"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/groq"
	"github.com/zendev-sh/goai/provider/minimax"
	"github.com/zendev-sh/goai/provider/mistral"
	"github.com/zendev-sh/goai/provider/nvidia"
	"github.com/zendev-sh/goai/provider/ollama"
	"github.com/zendev-sh/goai/provider/openai"
	"github.com/zendev-sh/goai/provider/openrouter"
	"github.com/zendev-sh/goai/provider/perplexity"
	"github.com/zendev-sh/goai/provider/runpod"
	"github.com/zendev-sh/goai/provider/together"
	"github.com/zendev-sh/goai/provider/vertex"
	"github.com/zendev-sh/goai/provider/vllm"
	"github.com/zendev-sh/goai/provider/xai"
)

// defaultCompatBaseURL is the fallback base URL when neither
// COMPAT_BASE_URL is set nor an explicit value is wired through. It
// matches llama.cpp's `llama-server` default port - by far the most
// common local OpenAI-compatible endpoint anyone running zenflow
// against a local model would have.
const defaultCompatBaseURL = "http://localhost:8080/v1"

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
	case "compat":
		// Generic OpenAI-compatible endpoint - local llama.cpp
		// `llama-server`, LiteLLM proxy, custom inference server, etc.
		// goai's compat package requires WithBaseURL (no built-in
		// default), so we read COMPAT_BASE_URL or fall back to
		// llama-server's default `http://localhost:8080/v1`. Optional
		// auth via COMPAT_API_KEY (most local servers don't check).
		baseURL := os.Getenv("COMPAT_BASE_URL")
		if baseURL == "" {
			baseURL = defaultCompatBaseURL
		}
		opts := []compat.Option{compat.WithBaseURL(baseURL)}
		if key := os.Getenv("COMPAT_API_KEY"); key != "" {
			opts = append(opts, compat.WithAPIKey(key))
		}
		return compat.Chat(modelID, opts...)
	case "ollama":
		// Ollama's OpenAI-compatible endpoint. Defaults to
		// `http://localhost:11434/v1` (baked into goai). Override via
		// OLLAMA_BASE_URL for a fully-qualified URL, or OLLAMA_HOST
		// (Ollama's standard env var - `host:port` without `/v1`,
		// which we append).
		var opts []ollama.Option
		if base := os.Getenv("OLLAMA_BASE_URL"); base != "" {
			opts = append(opts, ollama.WithBaseURL(base))
		} else if host := os.Getenv("OLLAMA_HOST"); host != "" {
			u := strings.TrimRight(host, "/")
			if !strings.HasSuffix(u, "/v1") {
				u += "/v1"
			}
			opts = append(opts, ollama.WithBaseURL(u))
		}
		return ollama.Chat(modelID, opts...)
	case "vllm":
		// vLLM's OpenAI-compatible endpoint. Defaults to
		// `http://localhost:8000/v1` (baked into goai). vLLM supports
		// optional API-key auth via `--api-key` flag - pick that up
		// from VLLM_API_KEY when set.
		var opts []vllm.Option
		if base := os.Getenv("VLLM_BASE_URL"); base != "" {
			opts = append(opts, vllm.WithBaseURL(base))
		}
		if key := os.Getenv("VLLM_API_KEY"); key != "" {
			opts = append(opts, vllm.WithAPIKey(key))
		}
		return vllm.Chat(modelID, opts...)
	case "anthropic":
		// Anthropic Messages API. Reads ANTHROPIC_API_KEY (and
		// optional ANTHROPIC_BASE_URL) from env.
		return anthropic.Chat(modelID)
	case "openai":
		// OpenAI Chat Completions API. Reads OPENAI_API_KEY (and
		// optional OPENAI_BASE_URL) from env. For Azure-hosted
		// OpenAI use the `azure/` or `azure-deployment/` prefix.
		return openai.Chat(modelID)
	case "xai":
		// xAI (Grok). Reads XAI_API_KEY.
		return xai.Chat(modelID)
	case "groq":
		// Groq LPU inference. Reads GROQ_API_KEY.
		return groq.Chat(modelID)
	case "cerebras":
		// Cerebras inference. Reads CEREBRAS_API_KEY.
		return cerebras.Chat(modelID)
	case "deepseek":
		// DeepSeek API. Reads DEEPSEEK_API_KEY.
		return deepseek.Chat(modelID)
	case "deepinfra":
		// DeepInfra serverless inference. Reads DEEPINFRA_API_KEY.
		return deepinfra.Chat(modelID)
	case "fireworks":
		// Fireworks AI inference. Reads FIREWORKS_API_KEY.
		return fireworks.Chat(modelID)
	case "together":
		// Together AI inference. Reads TOGETHER_AI_API_KEY (with
		// TOGETHER_API_KEY as fallback for backwards compatibility).
		return together.Chat(modelID)
	case "mistral":
		// Mistral La Plateforme. Reads MISTRAL_API_KEY.
		return mistral.Chat(modelID)
	case "cohere":
		// Cohere Chat v2. Reads COHERE_API_KEY.
		return cohere.Chat(modelID)
	case "perplexity":
		// Perplexity online inference. Reads PERPLEXITY_API_KEY.
		return perplexity.Chat(modelID)
	case "nvidia":
		// NVIDIA NIM / build.nvidia.com. Reads NVIDIA_API_KEY.
		return nvidia.Chat(modelID)
	case "openrouter":
		// OpenRouter (multi-provider router). Reads OPENROUTER_API_KEY.
		return openrouter.Chat(modelID)
	case "minimax":
		// MiniMax (Anthropic-compatible protocol). Reads MINIMAX_API_KEY.
		return minimax.Chat(modelID)
	case "fptcloud":
		// FPT Smart Cloud AI Marketplace (OpenAI-compatible).
		// Reads FPT_API_KEY and optional FPT_REGION (global|jp) and
		// FPT_BASE_URL.
		return fptcloud.Chat(modelID)
	case "cloudflare":
		// Cloudflare Workers AI. Reads CLOUDFLARE_API_TOKEN and
		// CLOUDFLARE_ACCOUNT_ID from env (both required for live
		// requests; constructor still returns non-nil if either is
		// unset and fails at request time).
		return cloudflare.Chat(modelID)
	case "runpod":
		// RunPod serverless inference. Needs both an endpoint ID and a
		// model ID; the endpoint ID lives in env (RUNPOD_ENDPOINT_ID)
		// because there's no clean way to pack it into the
		// `provider/model` string without inventing a new separator.
		// Reads RUNPOD_API_KEY for auth. If RUNPOD_ENDPOINT_ID is
		// unset the constructor still returns a non-nil model that
		// will fail at first request with a clear URL error.
		return runpod.Chat(os.Getenv("RUNPOD_ENDPOINT_ID"), modelID)
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
//
// Auto-detection is intentionally conservative: only a few well-known
// prefixes (gemini-, gpt-, claude-, grok-) plus Bedrock vendor-prefix
// dotted IDs are recognised. For everything else (qwen-*, llama-*,
// mixtral-*, deepseek-*, etc.) callers must use an explicit
// `provider/model` prefix because those names are served by multiple
// providers and we refuse to guess between them.
//
// For names served natively (claude-*, gpt-*), the provider's direct
// API key takes precedence over Azure when both are set; the previous
// behaviour silently routed them to Azure whenever AZURE_OPENAI_API_KEY
// was present, which surprised users who set ANTHROPIC_API_KEY or
// OPENAI_API_KEY specifically to use the native API.
func autoModelFromModelName(modelName string) provider.LanguageModel {
	hasGemini := os.Getenv("GEMINI_API_KEY") != "" || os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY") != ""
	hasBedrock := os.Getenv("AWS_ACCESS_KEY_ID") != ""
	hasAzure := os.Getenv("AZURE_OPENAI_API_KEY") != ""
	hasAnthropic := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasOpenAI := os.Getenv("OPENAI_API_KEY") != ""
	hasXAI := os.Getenv("XAI_API_KEY") != ""

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
		// Prefer native OpenAI over Azure when both are configured.
		if hasOpenAI {
			return openai.Chat(modelName)
		}
		if hasAzure {
			return azure.Chat(modelName, azure.WithDeploymentBasedURLs(true))
		}
	case len(modelName) >= 7 && modelName[:7] == "claude-":
		// Prefer native Anthropic, then Bedrock cross-region (for
		// dot-prefixed IDs we already matched above), then Azure.
		if hasAnthropic {
			return anthropic.Chat(modelName)
		}
		if hasAzure {
			return azure.Chat(modelName)
		}
	case len(modelName) >= 5 && modelName[:5] == "grok-":
		if hasXAI {
			return xai.Chat(modelName)
		}
	default:
		if hasAzure {
			return azure.Chat(modelName)
		}
	}

	// Fallback: try any available provider in a stable priority order.
	// Native single-vendor keys come before multi-tenant clouds so that
	// e.g. an OPENAI_API_KEY-only setup with a bare model name still
	// works without needing a provider prefix.
	if hasOpenAI {
		return openai.Chat(modelName)
	}
	if hasAnthropic {
		return anthropic.Chat(modelName)
	}
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
