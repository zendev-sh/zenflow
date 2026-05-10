package exec

// TopoSort performs topological sort on workflow steps using Kahn's algorithm.
// Returns ordered step IDs or CycleError if the graph contains a cycle.
func TopoSort(steps []Step) ([]string, error) {
	// Build adjacency list and in-degree map.
	inDegree := make(map[string]int, len(steps))
	adj := make(map[string][]string, len(steps))
	for _, s := range steps {
		inDegree[s.ID] = 0
	}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			adj[dep] = append(adj[dep], s.ID)
			inDegree[s.ID]++
		}
	}

	// Initialize queue with zero in-degree nodes.
	queue := make([]string, 0, len(steps))
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}

	order := make([]string, 0, len(steps))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		for _, neighbor := range adj[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(order) != len(steps) {
		// Collect nodes involved in the cycle.
		sorted := make(map[string]bool, len(order))
		for _, id := range order {
			sorted[id] = true
		}
		cycleNodes := make([]string, 0, len(steps)-len(order))
		for _, s := range steps {
			if !sorted[s.ID] {
				cycleNodes = append(cycleNodes, s.ID)
			}
		}
		return nil, &CycleError{
			Message: "workflow steps contain a dependency cycle",
			Nodes:   cycleNodes,
		}
	}
	return order, nil
}
