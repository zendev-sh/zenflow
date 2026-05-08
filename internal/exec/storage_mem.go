package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
)

// MemoryStorage implements Storage with in-memory maps.
type MemoryStorage struct {
	mu          sync.Mutex
	runs        map[string]*Run
	stepResults map[string]*StepResult       // key: "runID/stepID"
	sharedMem   map[string]map[string]string // key: runID
}

var _ Storage = (*MemoryStorage)(nil)

// NewMemoryStorage creates an empty in-memory storage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		runs:        make(map[string]*Run),
		stepResults: make(map[string]*StepResult),
		sharedMem:   make(map[string]map[string]string),
	}
}

// SaveRun stores a deep clone of run (thread-safe).
func (m *MemoryStorage) SaveRun(_ context.Context, run *Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := &Run{
		ID:       run.ID,
		Workflow: run.Workflow,
		Status:   run.Status,
		Steps:    make(map[string]*StepResult, len(run.Steps)),
	}
	for k, v := range run.Steps {
		clone.Steps[k] = cloneStepResult(v)
	}
	m.runs[run.ID] = clone
	return nil
}

// LoadRun returns a deep clone of run state. Returns ErrRunNotFound when the run does not exist.
func (m *MemoryStorage) LoadRun(_ context.Context, id string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return nil, fmt.Errorf("zenflow: run %q: %w", id, ErrRunNotFound)
	}
	clone := &Run{
		ID:       run.ID,
		Workflow: run.Workflow,
		Status:   run.Status,
		Steps:    make(map[string]*StepResult, len(run.Steps)),
	}
	for k, v := range run.Steps {
		clone.Steps[k] = cloneStepResult(v)
	}
	return clone, nil
}

// cloneStepResult creates a deep copy of a StepResult, including the Result map.
// (2026-05-04) - PreserveContent included so cumulative-loop
// results survive Save/Load through MemoryStorage with the same
// fidelity FileStorage now provides.
func cloneStepResult(sr *StepResult) *StepResult {
	if sr == nil {
		return nil
	}
	return &StepResult{
		ID:              sr.ID,
		Status:          sr.Status,
		Content:         sr.Content,
		Result:          cloneMapAny(sr.Result),
		Tokens:          sr.Tokens,
		Duration:        sr.Duration,
		Error:           sr.Error,
		PreserveContent: sr.PreserveContent,
	}
}

// cloneMapAny deep-copies a map[string]any via JSON round-trip.
// Returns nil if input is nil.
func cloneMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return maps.Clone(m)
	}
	var out map[string]any
	json.Unmarshal(data, &out) //nolint:errcheck // Marshal succeeded → Unmarshal cannot fail on same data
	return out
}

// SaveStepResult stores a deep clone of result under runID/stepID.
func (m *MemoryStorage) SaveStepResult(_ context.Context, runID, stepID string, result *StepResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stepResults[runID+"/"+stepID] = cloneStepResult(result)
	return nil
}

// LoadStepResult returns a clone. Returns ErrStepNotFound when no result is persisted.
func (m *MemoryStorage) LoadStepResult(_ context.Context, runID, stepID string) (*StepResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sr, ok := m.stepResults[runID+"/"+stepID]
	if !ok {
		return nil, fmt.Errorf("zenflow: step %q/%q: %w", runID, stepID, ErrStepNotFound)
	}
	return cloneStepResult(sr), nil
}

// SaveSharedMemory merges entries into the in-memory map for runID.
func (m *MemoryStorage) SaveSharedMemory(_ context.Context, runID string, entries map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.sharedMem[runID]
	if !ok {
		existing = make(map[string]string, len(entries))
		m.sharedMem[runID] = existing
	}
	for k, v := range entries {
		existing[k] = v
	}
	return nil
}

// LoadSharedMemory returns a clone of shared memory for runID; empty map if none.
func (m *MemoryStorage) LoadSharedMemory(_ context.Context, runID string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.sharedMem[runID]
	if !ok {
		return make(map[string]string), nil
	}
	return maps.Clone(mem), nil
}

// DeleteRun removes every record (run metadata, step results, shared
// memory) associated with runID. Added so long-lived embedders (web
// servers, daemons) can drop completed runs once the caller has
// consumed the result. Without this, the per-process MemoryStorage grew
// unboundedly: a server that handled 1000 workflows of 50 steps each
// retained 1000 *Run + 50000 *StepResult + N sharedMem maps in memory
// permanently. DeleteRun is a no-op when runID is unknown so callers
// can call it idempotently.
func (m *MemoryStorage) DeleteRun(runID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.runs, runID)
	prefix := runID + "/"
	for key := range m.stepResults {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(m.stepResults, key)
		}
	}
	delete(m.sharedMem, runID)
}
