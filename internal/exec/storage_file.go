package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileStorage implements Storage with JSON file persistence.
// Concurrency: a single sync.RWMutex serialises writes against
// concurrent reads/writes to the same baseDir. Reads (LoadRun,
// LoadStepResult, LoadSharedMemory) take RLock so multiple
// concurrent reads can proceed in parallel; writes (SaveRun,
// SaveStepResult, SaveSharedMemory) take Lock. This avoids
// serialising parallel-step completion through a single I/O lane
// at the cost of having writes still serialised - a per-runID
// or per-key mutex would unlock more parallelism but adds
// complexity not yet warranted at the OSS-launch baseline.
type FileStorage struct {
	mu      sync.RWMutex
	baseDir string
}

var _ Storage = (*FileStorage)(nil)

// NewFileStorage creates a new FileStorage that persists data under baseDir.
func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
}

// fileRun is the JSON-serializable representation of Run.
type fileRun struct {
	ID       string                 `json:"id"`
	Workflow *Workflow              `json:"workflow"`
	Status   WorkflowStatus         `json:"status"`
	Steps    map[string]*fileStepRS `json:"steps"`
}

// fileStepRS is the JSON-serializable representation of StepResult.
// PreserveContent is included so cumulative-loop step results round-trip
// across Save/Load without truncation. Loop steps with outputMode:
// cumulative set this flag at executor.go to bypass the 16 KB per-dep
// truncation cap and the OutputTransform. Without persistence the flag
// was lost across resume, downstream dependents saw the cumulative
// content truncated, defeating the whole point of cumulative mode.
// Tokens uses tokensSchema (not provider.Usage directly) to pin stable
// lowercase snake_case JSON keys independent of upstream tag changes.
// Error uses errorSchema (not a plain string) to preserve errors.Join
// multi-error structure across the Save/Load round-trip.
type fileStepRS struct {
	ID              string         `json:"id"`
	Status          StepStatus     `json:"status"`
	Content         string         `json:"content,omitempty"`
	Result          map[string]any `json:"result,omitempty"`
	Tokens          tokensSchema   `json:"tokens"`
	DurationNs      int64          `json:"durationNs"`
	Error           *errorSchema   `json:"error,omitempty"`
	PreserveContent bool           `json:"preserveContent,omitempty"`
}

func toFileStep(sr *StepResult) *fileStepRS {
	fs := &fileStepRS{
		ID:              sr.ID,
		Status:          sr.Status,
		Content:         sr.Content,
		Result:          sr.Result,
		Tokens:          tokensToSchema(sr.Tokens),
		DurationNs:      int64(sr.Duration),
		PreserveContent: sr.PreserveContent,
	}
	if sr.Error != nil {
		s := errorToSchema(sr.Error)
		fs.Error = &s
	}
	return fs
}

func fromFileStep(fs *fileStepRS) *StepResult {
	sr := &StepResult{
		ID:              fs.ID,
		Status:          fs.Status,
		Content:         fs.Content,
		Result:          fs.Result,
		Tokens:          tokensFromSchema(fs.Tokens),
		Duration:        time.Duration(fs.DurationNs),
		PreserveContent: fs.PreserveContent,
	}
	if fs.Error != nil {
		sr.Error = errorFromSchema(*fs.Error)
	}
	return sr
}

func toFileRun(run *Run) *fileRun {
	fr := &fileRun{
		ID:       run.ID,
		Workflow: run.Workflow,
		Status:   run.Status,
		Steps:    make(map[string]*fileStepRS, len(run.Steps)),
	}
	for k, v := range run.Steps {
		fr.Steps[k] = toFileStep(v)
	}
	return fr
}

func fromFileRun(fr *fileRun) *Run {
	run := &Run{
		ID:       fr.ID,
		Workflow: fr.Workflow,
		Status:   fr.Status,
		Steps:    make(map[string]*StepResult, len(fr.Steps)),
	}
	for k, v := range fr.Steps {
		run.Steps[k] = fromFileStep(v)
	}
	return run
}

// SaveRun atomically persists run state to <baseDir>/<runID>/run.json.
func (f *FileStorage) SaveRun(_ context.Context, run *Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Join(f.baseDir, run.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}

	fr := toFileRun(run)
	return atomicWriteJSON(filepath.Join(dir, "run.json"), fr)
}

// LoadRun reads and deserializes run state. Returns ErrRunNotFound when the run does not exist.
func (f *FileStorage) LoadRun(_ context.Context, id string) (*Run, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path := filepath.Join(f.baseDir, id, "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("zenflow: run %q: %w", id, ErrRunNotFound)
		}
		return nil, fmt.Errorf("load run %q: %w", id, err)
	}

	var fr fileRun
	if err := json.Unmarshal(data, &fr); err != nil {
		return nil, fmt.Errorf("decode run %q: %w", id, err)
	}
	return fromFileRun(&fr), nil
}

// SaveStepResult atomically writes the step result to <baseDir>/<runID>/steps/<stepID>.json.
func (f *FileStorage) SaveStepResult(_ context.Context, runID, stepID string, result *StepResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Join(f.baseDir, runID, "steps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create steps dir: %w", err)
	}

	fs := toFileStep(result)
	return atomicWriteJSON(filepath.Join(dir, stepID+".json"), fs)
}

// LoadStepResult reads a step result. Returns ErrStepNotFound when no result is persisted.
func (f *FileStorage) LoadStepResult(_ context.Context, runID, stepID string) (*StepResult, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path := filepath.Join(f.baseDir, runID, "steps", stepID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("zenflow: step %q/%q: %w", runID, stepID, ErrStepNotFound)
		}
		return nil, fmt.Errorf("load step result %q/%q: %w", runID, stepID, err)
	}

	var fs fileStepRS
	if err := json.Unmarshal(data, &fs); err != nil {
		return nil, fmt.Errorf("decode step result %q/%q: %w", runID, stepID, err)
	}
	return fromFileStep(&fs), nil
}

// SaveSharedMemory merges entries into the persisted shared-memory JSON for runID.
// FileStorage assumes single-process access to baseDir. Concurrent writes
// from multiple processes can race during the read-modify-write of shared
// memory; the in-process mutex does not protect against that.
func (f *FileStorage) SaveSharedMemory(_ context.Context, runID string, entries map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	dir := filepath.Join(f.baseDir, runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}

	path := filepath.Join(dir, "shared_memory.json")

	// Load existing entries and merge.
	existing := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing shared memory: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("decode shared memory: %w", err)
		}
	}

	for k, v := range entries {
		existing[k] = v
	}

	return atomicWriteJSON(path, existing)
}

// LoadSharedMemory returns shared-memory entries for runID; empty map if none saved.
func (f *FileStorage) LoadSharedMemory(_ context.Context, runID string) (map[string]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	path := filepath.Join(f.baseDir, runID, "shared_memory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("load shared memory: %w", err)
	}

	var mem map[string]string
	if err := json.Unmarshal(data, &mem); err != nil {
		return nil, fmt.Errorf("decode shared memory: %w", err)
	}
	return mem, nil
}

// writeCloseSyncer abstracts file operations for testability.
type writeCloseSyncer interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

// createTempFile is injectable for testing. Production uses os.CreateTemp.
var createTempFile = func(dir, pattern string) (writeCloseSyncer, error) {
	return os.CreateTemp(dir, pattern)
}

// atomicWriteJSON marshals v to JSON and writes it atomically to path.
// Sequence: write to temp → fsync → close → rename (POSIX atomic).
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}

	dir := filepath.Dir(path)
	f, err := createTempFile(dir, ".zenflow-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
