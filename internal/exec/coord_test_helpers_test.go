package exec

import (
	"context"
	"testing"

	"github.com/zendev-sh/goai/provider"
)

// This file provides after test helpers that replace the legacy
// CoordinatorAgent stand-ins (NoopCoordinator, scriptedCoordinator,
// stubMessagingCoordinator, etc.). changed WithCoordinator's
// argument from a CoordinatorAgent interface to a *AgentRunner.
// Production code paths that previously type-asserted against
// NoopCoordinator now check Coordinator == nil; tests that just need
// "any non-nil coord" to enable the executor's mailbox + router stack
// construct one of these helpers.

// stubCoordLanguageModel is a minimal provider.LanguageModel
// implementation used by tests that build a coordinator runner without
// exercising the LLM transport. If any caller accidentally invokes
// DoGenerate or DoStream, the embedded *testing.T fails the test so
// the bug surfaces at the call site instead of as a nil panic.
type stubCoordLanguageModel struct {
	t *testing.T
}

func (s stubCoordLanguageModel) ModelID() string { return "stub-model" }
func (s stubCoordLanguageModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	if s.t != nil {
		s.t.Fatalf("stubCoordLanguageModel.DoGenerate must not be called")
	}
	return nil, nil
}
func (s stubCoordLanguageModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	if s.t != nil {
		s.t.Fatalf("stubCoordLanguageModel.DoStream must not be called")
	}
	return nil, nil
}

// newTestCoordRunner returns a minimal *AgentRunner suitable for use
// as the Executor.Coordinator field on tests that need the mailbox +
// router + delivery-engine stack to be allocated. The runner has a
// fresh InMemoryMailboxStore (so pushCoordEvent calls succeed)
// and a coord step ID of "coordinator" (the default convention from
// coordStepID when StepID is empty).
// The runner is NOT started - no Run goroutine. documents that
// the orchestrator never calls Run on the coord runner; tests follow
// the same contract and inspect the mailbox directly when they need
// to assert pushed events.
func newTestCoordRunner() *AgentRunner {
	return &AgentRunner{
		stepID:  "coordinator",
		mailbox: NewInMemoryMailboxStore(),
	}
}
