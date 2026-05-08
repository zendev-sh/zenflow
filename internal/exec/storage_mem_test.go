package exec

import (
	"errors"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

func TestMemoryStorage_SaveLoadRun(t *testing.T) {
	store := NewMemoryStorage()

	run := &Run{
		ID:       "run-1",
		Workflow: &Workflow{Name: "test-wf"},
		Status:   spec.StatusCompleted,
		Steps:    map[string]*StepResult{},
	}

	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	loaded, err := store.LoadRun(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}

	if loaded.ID != run.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, run.ID)
	}
	if loaded.Status != run.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, run.Status)
	}
	if loaded.Workflow.Name != run.Workflow.Name {
		t.Errorf("Workflow.Name = %q, want %q", loaded.Workflow.Name, run.Workflow.Name)
	}
}

func TestMemoryStorage_LoadNotFound(t *testing.T) {
	store := NewMemoryStorage()

	_, err := store.LoadRun(t.Context(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing run, got nil")
	}
}

func TestMemoryStorage_SaveLoadStepResult(t *testing.T) {
	store := NewMemoryStorage()

	sr := &StepResult{
		ID:       "step-1",
		Status:   spec.StepCompleted,
		Content:  "hello world",
		Tokens:   provider.Usage{InputTokens: 100, OutputTokens: 50},
		Duration: 5 * time.Second,
	}

	if err := store.SaveStepResult(t.Context(), "run-1", "step-1", sr); err != nil {
		t.Fatalf("SaveStepResult: %v", err)
	}

	loaded, err := store.LoadStepResult(t.Context(), "run-1", "step-1")
	if err != nil {
		t.Fatalf("LoadStepResult: %v", err)
	}

	if loaded.ID != sr.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, sr.ID)
	}
	if loaded.Status != sr.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, sr.Status)
	}
	if loaded.Content != sr.Content {
		t.Errorf("Content = %q, want %q", loaded.Content, sr.Content)
	}
	if loaded.Tokens.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", loaded.Tokens.InputTokens)
	}
}

func TestMemoryStorage_StepResultNotFound(t *testing.T) {
	store := NewMemoryStorage()

	_, err := store.LoadStepResult(t.Context(), "run-1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing step result, got nil")
	}
}

func TestMemoryStorage_SharedMemory(t *testing.T) {
	s := NewMemoryStorage()
	ctx := t.Context()

	entries := map[string]string{"key1": "val1", "key2": "val2"}
	if err := s.SaveSharedMemory(ctx, "run1", entries); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.LoadSharedMemory(ctx, "run1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["key1"] != "val1" || got["key2"] != "val2" {
		t.Errorf("loaded = %v, want %v", got, entries)
	}
}

func TestMemoryStorage_SharedMemoryNotFound(t *testing.T) {
	s := NewMemoryStorage()
	got, err := s.LoadSharedMemory(t.Context(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestMemoryStorage_SharedMemoryReturnsCopy(t *testing.T) {
	s := NewMemoryStorage()
	ctx := t.Context()

	if err := s.SaveSharedMemory(ctx, "run1", map[string]string{"key1": "val1"}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.LoadSharedMemory(ctx, "run1")
	got["key1"] = "mutated" // Should not affect internal storage.

	got2, _ := s.LoadSharedMemory(ctx, "run1")
	if got2["key1"] != "val1" {
		t.Errorf("LoadSharedMemory returned mutable reference: key1 = %q, want val1", got2["key1"])
	}
}

func TestMemoryStorage_SharedMemoryMerge(t *testing.T) {
	s := NewMemoryStorage()
	ctx := t.Context()

	// First save: key1.
	if err := s.SaveSharedMemory(ctx, "run1", map[string]string{"key1": "val1"}); err != nil {
		t.Fatal(err)
	}
	// Second save: key2 - should merge, not overwrite.
	if err := s.SaveSharedMemory(ctx, "run1", map[string]string{"key2": "val2"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadSharedMemory(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if got["key1"] != "val1" {
		t.Errorf("key1 lost after second save: %v", got)
	}
	if got["key2"] != "val2" {
		t.Errorf("key2 missing: %v", got)
	}
}

func TestCloneStepResult_Nil(t *testing.T) {
	result := cloneStepResult(nil)
	if result != nil {
		t.Errorf("cloneStepResult(nil) = %v, want nil", result)
	}
}

func TestCloneMapAny_Nil(t *testing.T) {
	result := cloneMapAny(nil)
	if result != nil {
		t.Errorf("cloneMapAny(nil) = %v, want nil", result)
	}
}

func TestCloneMapAny_MarshalFailureFallback(t *testing.T) {
	// json.Marshal fails for channels - triggers the shallow clone fallback.
	ch := make(chan int)
	m := map[string]any{"ch": ch, "k": "v"}
	result := cloneMapAny(m)
	if result == nil {
		t.Fatal("expected non-nil fallback clone")
	}
	// Shallow clone preserves the SAME channel value (json round-trip
	// would have replaced it with nil/zero). Asserting identity proves
	// the maps.Clone fallback path was taken, not just "result != nil".
	gotCh, ok := result["ch"].(chan int)
	if !ok {
		t.Fatalf("result[ch] type = %T, want chan int (fallback path drops type info)", result["ch"])
	}
	if gotCh != ch {
		t.Error("result[ch] is not the same channel as input - shallow clone broken")
	}
	if result["k"] != "v" {
		t.Errorf("result[k] = %v, want v", result["k"])
	}
}

func TestMemoryStorage_LoadRun_NotFound(t *testing.T) {
	store := NewMemoryStorage()
	_, err := store.LoadRun(t.Context(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent run")
	}
	// Tighten: caller contract is errors.Is(err, ErrRunNotFound), not
	// "any error". Catches a regression where the code returns a generic
	// error and breaks every caller's errors.Is check.
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("errors.Is(err, ErrRunNotFound) = false; err = %v", err)
	}
}

func TestMemoryStorage_SaveLoadRun_WithSteps(t *testing.T) {
	store := NewMemoryStorage()
	ctx := t.Context()

	run := &Run{
		ID:       "run-steps",
		Workflow: &Workflow{Name: "test-wf"},
		Status:   spec.StatusCompleted,
		Steps: map[string]*StepResult{
			"s1": {ID: "s1", Status: spec.StepCompleted, Content: "done", Result: map[string]any{"key": "val"}},
			"s2": {ID: "s2", Status: spec.StepFailed, Error: errors.New("oops")},
		},
	}

	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	loaded, err := store.LoadRun(ctx, "run-steps")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if len(loaded.Steps) != 2 {
		t.Fatalf("Steps len = %d, want 2", len(loaded.Steps))
	}
	if loaded.Steps["s1"].Content != "done" {
		t.Errorf("s1.Content = %q, want 'done'", loaded.Steps["s1"].Content)
	}
	if loaded.Steps["s2"].Error == nil {
		t.Error("s2.Error should be non-nil")
	}
}

func TestCloneMapAny_ValidData(t *testing.T) {
	// Exercise the successful JSON round-trip path (return out).
	m := map[string]any{"key": "value", "num": float64(42)}
	result := cloneMapAny(m)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result["key"] != "value" {
		t.Errorf("key = %v, want 'value'", result["key"])
	}
	if result["num"] != float64(42) {
		t.Errorf("num = %v, want 42", result["num"])
	}
}

// TestErrRunNotFound asserts that MemoryStorage.LoadRun wraps ErrRunNotFound.
func TestErrRunNotFound(t *testing.T) {
	store := NewMemoryStorage()
	_, err := store.LoadRun(t.Context(), "no-such-run")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("errors.Is(err, ErrRunNotFound) = false; err = %v", err)
	}
}

// TestErrStepNotFound asserts that MemoryStorage.LoadStepResult wraps ErrStepNotFound.
func TestErrStepNotFound(t *testing.T) {
	store := NewMemoryStorage()
	_, err := store.LoadStepResult(t.Context(), "run-1", "no-such-step")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrStepNotFound) {
		t.Errorf("errors.Is(err, ErrStepNotFound) = false; err = %v", err)
	}
}
