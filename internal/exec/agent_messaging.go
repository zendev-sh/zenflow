package exec

// agent_messaging.go - the per-call messaging substrate for a
// standalone agent run (AUV2 PR4 "chat with a running agent").
//
// A workflow step agent reaches its mailbox through the executor's
// DeliveryEngine. A standalone agent spawned via RunAgent /
// RunAgentAsync has no executor, so the substrate is built per-call:
// one MessageRouter, one in-memory MailboxStore, one wake channel and
// the derived primary step ID. RunAgentAsync builds it up front and
// attaches it to the AgentHandle so AgentHandle.SendMessage can
// address the running agent's mailbox directly.

// agentMessaging bundles the router, mailbox, wake channel, primary
// step ID and run ID for one standalone agent invocation. The same
// instance is shared between the AgentHandle (for SendMessage) and the
// inner runAgent call (which wires it into the AgentRunner).
type agentMessaging struct {
	runID         string
	router        *MessageRouter
	mailbox       MailboxStore
	wake          chan struct{}
	primaryStepID string
}

// newAgentMessaging builds a fresh messaging substrate for runID. The
// router gets an in-memory mailbox; the primary step ID is registered
// as both a step and an inbox so router.Send addressed to it is
// accepted. The wake channel is cap-1 buffered, matching the runner's
// mailbox-mode contract (a pending signal coalesces - the runner
// drains the whole mailbox per wake).
func newAgentMessaging(runID string) *agentMessaging {
	r := NewMessageRouter()
	mb := NewInMemoryMailboxStore()
	r.SetMailbox(mb)
	stepID := agentPrimaryStepID(runID)
	r.RegisterStep(stepID)
	r.RegisterInbox(stepID)
	return &agentMessaging{
		runID:         runID,
		router:        r,
		mailbox:       mb,
		wake:          make(chan struct{}, 1),
		primaryStepID: stepID,
	}
}
