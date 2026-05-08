package exec

import "strings"

// agent_test_helpers_test.go holds test-only accessors that summarise
// MessageRouter state via the public Inboxes method.

// runAgentPrimaryStepID returns the StepID RunAgent registered as the
// per-call primary inbox on the supplied router. Tests that consult
// this helper after a RunAgent call rely on the WithRouterObserver
// hook to capture the router.
// Filter by the "agent:primary:" prefix so the helper returns the
// primary inbox even when sibling/child inboxes are also registered on
// the same router.
func runAgentPrimaryStepID(router *MessageRouter) string {
	if router == nil {
		return ""
	}
	for _, id := range router.Inboxes() {
		if strings.HasPrefix(id, "agent:primary:") {
			return id
		}
	}
	return ""
}

// routerOpenInboxes returns a snapshot of every inbox-shaped step ID
// currently tracked on the router (open + closed). Used by the D-track
// sibling share test to assert that two children spawned by the same
// parent landed inboxes on the SAME router instance.
func routerOpenInboxes(router *MessageRouter) []string {
	if router == nil {
		return nil
	}
	return router.Inboxes()
}
