package exec

// agent_primary_id.go holds the stable primary-runner StepID derivation
// used as the mailbox key for zenflow's RunAgent path. The single
// function agentPrimaryStepID maps a runID to a deterministic key so
// RegisterInbox and the runner drain target the same mailbox.

// agentPrimaryStepID derives the stable primary-runner StepID used as the
// mailbox key for the RunAgent invocation identified by runID. The exact
// format is internal but must be reproducible for the lifetime of one Run
// so RegisterInbox / Close target the same key the runner uses to drain.
func agentPrimaryStepID(runID string) string {
	if runID == "" {
		return "agent:primary"
	}
	return "agent:primary:" + runID
}
