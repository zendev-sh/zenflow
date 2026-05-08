package exec

import (
	"errors"
	"testing"
)

func TestTopoSort_Linear(t *testing.T) {
	steps := []Step{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	order, err := TopoSort(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(order) != len(want) {
		t.Fatalf("len = %d, want %d", len(order), len(want))
	}
	for i, id := range want {
		if order[i] != id {
			t.Errorf("order[%d] = %q, want %q", i, order[i], id)
		}
	}
}

func TestTopoSort_Diamond(t *testing.T) {
	// A → {B, C} → D
	steps := []Step{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"b", "c"}},
	}
	order, err := TopoSort(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 4 {
		t.Fatalf("len = %d, want 4", len(order))
	}

	// Build position map to verify ordering constraints.
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}

	// A must come before B and C.
	if pos["a"] >= pos["b"] {
		t.Errorf("a (pos %d) should come before b (pos %d)", pos["a"], pos["b"])
	}
	if pos["a"] >= pos["c"] {
		t.Errorf("a (pos %d) should come before c (pos %d)", pos["a"], pos["c"])
	}
	// B and C must come before D.
	if pos["b"] >= pos["d"] {
		t.Errorf("b (pos %d) should come before d (pos %d)", pos["b"], pos["d"])
	}
	if pos["c"] >= pos["d"] {
		t.Errorf("c (pos %d) should come before d (pos %d)", pos["c"], pos["d"])
	}
}

func TestTopoSort_NoDeps(t *testing.T) {
	steps := []Step{
		{ID: "a"},
		{ID: "b"},
		{ID: "c"},
	}
	order, err := TopoSort(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("len = %d, want 3", len(order))
	}
	// All three must be present (any order).
	seen := make(map[string]bool)
	for _, id := range order {
		seen[id] = true
	}
	for _, id := range []string{"a", "b", "c"} {
		if !seen[id] {
			t.Errorf("missing step %q in output", id)
		}
	}
}

func TestTopoSort_Cycle(t *testing.T) {
	steps := []Step{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}
	_, err := TopoSort(steps)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	var target *CycleError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *CycleError", err)
	}
}

func TestTopoSort_SelfDep(t *testing.T) {
	steps := []Step{
		{ID: "a", DependsOn: []string{"a"}},
	}
	_, err := TopoSort(steps)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	var target *CycleError
	if !errors.As(err, &target) {
		t.Errorf("error type = %T, want *CycleError", err)
	}
}

func TestTopoSort_Empty(t *testing.T) {
	order, err := TopoSort(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 0 {
		t.Errorf("len = %d, want 0", len(order))
	}
}

func TestTopoSort_SingleStep(t *testing.T) {
	steps := []Step{{ID: "only"}}
	order, err := TopoSort(steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 1 || order[0] != "only" {
		t.Errorf("order = %v, want [only]", order)
	}
}

func TestTopoSort_CycleNodes(t *testing.T) {
	steps := []Step{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
		{ID: "d"}, // not in cycle
	}
	_, err := TopoSort(steps)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	var cycleErr *CycleError
	if !errors.As(err, &cycleErr) {
		t.Fatalf("error type = %T, want *CycleError", err)
	}
	if len(cycleErr.Nodes) != 3 {
		t.Errorf("cycle nodes = %v, want 3 nodes (a, b, c)", cycleErr.Nodes)
	}
	// d should NOT be in the cycle.
	for _, n := range cycleErr.Nodes {
		if n == "d" {
			t.Errorf("node 'd' should not be in cycle, got %v", cycleErr.Nodes)
		}
	}
}
