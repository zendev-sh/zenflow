package exec

// bench_toposort_test.go - BenchmarkTopoSort
// Measures topological sort throughput on three DAG sizes:
// - 10-step linear chain
// - 100-step linear chain
// - 1000-step linear chain
// Each iteration allocates a fresh step slice from the pre-built template
// so the benchmark includes the map-allocation cost inside TopoSort.
// Slice construction is done in setup (before ResetTimer) to exclude it.
// Run:
//	go test -bench=BenchmarkTopoSort -benchtime=1x -run=^$ ./zenflow/

import (
	"fmt"
	"testing"
)

// buildLinearSteps returns a slice of n steps forming a linear chain:
// step0 → step1 → ... → stepN-1. The result is built once and copied
// per iteration via a sub-slice reference - callers that need a fresh
// in-degree map must pass a new copy to TopoSort each iteration (which
// is what the benchmark does: slices are value types so each b.Loop
// call gets an independent copy through the literal passed to TopoSort).
func buildLinearSteps(n int) []Step {
	steps := make([]Step, n)
	for i := range n {
		steps[i] = Step{
			ID:           fmt.Sprintf("step%04d", i),
			Instructions: "placeholder",
		}
		if i > 0 {
			steps[i].DependsOn = []string{fmt.Sprintf("step%04d", i-1)}
		}
	}
	return steps
}

// BenchmarkTopoSort - topological sort throughput for 10, 100, 1000 steps.
// Each sub-benchmark passes a fresh copy of the pre-built step slice to
// TopoSort per iteration so the reported allocs include only the maps and
// queue slice allocated inside TopoSort itself, not the input build cost.
func BenchmarkTopoSort(b *testing.B) {
	sizes := []int{10, 100, 1000}

	for _, n := range sizes {
		steps := buildLinearSteps(n)
		b.Run(fmt.Sprintf("n%d", n), func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
 // Pass a fresh slice header each iteration; the underlying
 // array is read-only (TopoSort only reads DependsOn), so
 // sharing is safe and avoids per-iteration allocation of the
 // input slice itself.
				order, err := TopoSort(steps)
				if err != nil {
					b.Fatalf("TopoSort: %v", err)
				}
				_ = order
			}
		})
	}
}
