package exec

import (
	"cmp"
	"slices"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// markDependents recursively marks all transitive dependents of a step in the given set.
func (e *Executor) markDependents(stepID string, dependents map[string][]string, set map[string]bool) {
	for _, dep := range dependents[stepID] {
		if !set[dep] {
			set[dep] = true
			e.markDependents(dep, dependents, set)
		}
	}
}

// scheduleOrder reorders ready steps based on the configured scheduler strategy.
// running maps stepID->true for currently in-flight steps.
// For dependency-first (default), the original order is preserved.
// For round-robin, steps are interleaved by agent to avoid consecutive same-agent dispatch.
// For least-busy, steps are sorted so agents with fewer running steps go first.
func (e *Executor) scheduleOrder(ready []Step, running map[string]bool) []Step {
	strategy := e.Workflow.Options.Scheduler
	switch strategy {
	case spec.SchedulerRoundRobin:
		return scheduleRoundRobin(ready)
	case spec.SchedulerLeastBusy:
		return scheduleLeastBusy(ready, running, e.Workflow.Steps)
	default:
 // dependency-first or empty: topological order (as-is).
		return ready
	}
}

// scheduleRoundRobin interleaves steps by agent name so that consecutive
// dispatches prefer different agents. Steps within each agent bucket maintain
// their original relative order.
func scheduleRoundRobin(ready []Step) []Step {
	if len(ready) <= 1 {
		return ready
	}
	// Group steps by agent.
	buckets := make(map[string][]Step, len(ready))
	agentOrder := make([]string, 0, len(ready))
	for _, s := range ready {
		if _, seen := buckets[s.Agent]; !seen {
			agentOrder = append(agentOrder, s.Agent)
		}
		buckets[s.Agent] = append(buckets[s.Agent], s)
	}
	// Interleave: pick one from each bucket in round-robin.
	result := make([]Step, 0, len(ready))
	idx := make(map[string]int, len(agentOrder))
	for len(result) < len(ready) {
		for _, agent := range agentOrder {
			i := idx[agent]
			if i < len(buckets[agent]) {
				result = append(result, buckets[agent][i])
				idx[agent] = i + 1
			}
		}
	}
	return result
}

// scheduleLeastBusy sorts ready steps so that agents with fewer currently
// running steps are dispatched first.
func scheduleLeastBusy(ready []Step, running map[string]bool, allSteps []Step) []Step {
	if len(ready) <= 1 {
		return ready
	}
	// Count running steps per agent.
	agentLoad := make(map[string]int, len(allSteps))
	stepAgent := make(map[string]string, len(allSteps))
	for _, s := range allSteps {
		stepAgent[s.ID] = s.Agent
	}
	for id := range running {
		agentLoad[stepAgent[id]]++
	}
	// Stable sort by agent load (ascending).
	sorted := make([]Step, len(ready))
	copy(sorted, ready)
	slices.SortStableFunc(sorted, func(a, b Step) int {
		return cmp.Compare(agentLoad[a.Agent], agentLoad[b.Agent])
	})
	return sorted
}
