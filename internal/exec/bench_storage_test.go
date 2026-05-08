package exec

// bench_storage_test.go - BenchmarkStorageSaveLoad
// Measures the SaveRun → LoadRun round-trip cost for two backends:
// - memory: MemoryStorage (in-process maps, JSON clone via json.Marshal)
// - file: FileStorage (JSON files written to os.TempDir)
// The Run fixture is a small workflow with three completed steps,
// created once in setup (before ResetTimer). Each iteration saves the
// same run under a fixed ID and then loads it back; the loaded value
// is discarded.
// Run:
//	go test -bench=BenchmarkStorageSaveLoad -benchtime=1x -run=^$ ./zenflow/

import (
	"context"
	"os"
	"testing"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// benchRun is a small completed workflow run used as fixture.
// Returns a freshly-allocated value so sub-benchmarks don't share
// mutable pointer state.
func benchRun() *Run {
	wf := &Workflow{
		Name: "bench-storage-wf",
		Steps: []Step{
			{ID: "step-a", Instructions: "do a"},
			{ID: "step-b", Instructions: "do b", DependsOn: []string{"step-a"}},
			{ID: "step-c", Instructions: "do c", DependsOn: []string{"step-b"}},
		},
	}
	return &Run{
		ID:       "bench-run-001",
		Workflow: wf,
		Status:   spec.StatusCompleted,
		Steps: map[string]*StepResult{
			"step-a": {ID: "step-a", Status: spec.StepCompleted, Content: "result A"},
			"step-b": {ID: "step-b", Status: spec.StepCompleted, Content: "result B"},
			"step-c": {ID: "step-c", Status: spec.StepCompleted, Content: "result C"},
		},
	}
}

// BenchmarkStorageSaveLoad - SaveRun → LoadRun round-trip per backend.
func BenchmarkStorageSaveLoad(b *testing.B) {
	ctx := context.Background()

	b.Run("memory", func(b *testing.B) {
		store := NewMemoryStorage()
		run := benchRun()
 // Pre-seed so LoadRun on the very first iteration finds the row.
		if err := store.SaveRun(ctx, run); err != nil {
			b.Fatalf("pre-seed SaveRun: %v", err)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for b.Loop() {
			if err := store.SaveRun(ctx, run); err != nil {
				b.Fatalf("SaveRun: %v", err)
			}
			loaded, err := store.LoadRun(ctx, run.ID)
			if err != nil {
				b.Fatalf("LoadRun: %v", err)
			}
			_ = loaded
		}
	})

	b.Run("file", func(b *testing.B) {
		dir, err := os.MkdirTemp("", "bench-filestorage-*")
		if err != nil {
			b.Fatalf("MkdirTemp: %v", err)
		}
		b.Cleanup(func() { _ = os.RemoveAll(dir) })

		store := NewFileStorage(dir)
		run := benchRun()
 // Pre-seed.
		if err := store.SaveRun(ctx, run); err != nil {
			b.Fatalf("pre-seed SaveRun: %v", err)
		}
		b.ResetTimer()
		b.ReportAllocs()
		for b.Loop() {
			if err := store.SaveRun(ctx, run); err != nil {
				b.Fatalf("SaveRun: %v", err)
			}
			loaded, err := store.LoadRun(ctx, run.ID)
			if err != nil {
				b.Fatalf("LoadRun: %v", err)
			}
			_ = loaded
		}
	})
}
