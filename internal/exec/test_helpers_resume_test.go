package exec

import (
	"github.com/zendev-sh/goai/provider"
)

// mkTextMsg is a small constructor used by transcript-resume tests at
// the root package level. The transcript implementation lives in
// internal/resume/, but several executor / agent_runner tests still
// build messages directly through the root package.
func mkTextMsg(role provider.Role, text string) provider.Message {
	return provider.Message{
		Role:    role,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}
