package exec

import (
	"testing"

	"github.com/zendev-sh/goai/provider"
)

func TestIsReasoningModel(t *testing.T) {
	cases := map[string]bool{
		// Reasoning families (provider-prefixed and bare).
		"azure/gpt-5.5":          true,
		"gpt-5":                  true,
		"gpt-5.5-2026-04-24":     true,
		"gpt-5-mini":             true,
		"azure-deployment/gpt-5": true,
		"openai/o3-mini":         true,
		"o1":                     true,
		"o4-mini":                true,
		"gpt-oss-safeguard":      true,
		"GPT-5.5":                true, // case-insensitive
		"  azure/gpt-5.5  ":      true, // trimmed
		// Not reasoning models.
		"azure/gpt-5-chat":                       false, // chat variant accepts sampling
		"gpt-5.2-chat-2026-02-10":                false,
		"azure/gpt-4.1":                          false,
		"google/gemini-2.5-flash":                false,
		"bedrock/jp.anthropic.claude-sonnet-4-6": false,
		"o1abc":                                  false, // no separator after prefix
		"":                                       false,
	}
	for in, want := range cases {
		if got := isReasoningModel(in); got != want {
			t.Errorf("isReasoningModel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestRun_StripsSamplingForReasoningModel verifies the flow/agent path drops
// temperature/top_p for reasoning models (which reject them) while preserving
// them for ordinary models.
func TestRun_StripsSamplingForReasoningModel(t *testing.T) {
	temp := 0.9
	topp := 0.5
	cases := []struct {
		name         string
		model        string
		wantStripped bool
	}{
		{"reasoning gpt-5.5 stripped", "azure/gpt-5.5", true},
		{"reasoning o3-mini stripped", "openai/o3-mini", true},
		{"gpt-5-chat preserved", "azure/gpt-5-chat", false},
		{"normal model preserved", "google/gemini-2.5-flash", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &mockModel{responses: []*provider.GenerateResult{textResult("ok", 1, 1)}}
			r := &AgentRunner{model: model}
			cfg := AgentConfig{Temperature: &temp, TopP: &topp, MaxTurns: 1}
			if _, err := r.Run(t.Context(), cfg, "hi", tc.model, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := model.getCalls()
			if len(calls) == 0 {
				t.Fatal("no generate calls recorded")
			}
			gotTemp, gotTopP := calls[0].Temperature, calls[0].TopP
			if tc.wantStripped {
				if gotTemp != nil || gotTopP != nil {
					t.Errorf("model %q: expected sampling stripped, got temp=%v topP=%v", tc.model, gotTemp, gotTopP)
				}
			} else {
				if gotTemp == nil || gotTopP == nil {
					t.Errorf("model %q: expected sampling preserved, got temp=%v topP=%v", tc.model, gotTemp, gotTopP)
				}
			}
		})
	}
}
