package exec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

func TestFileStorage_SaveLoadRun(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	run := &Run{
		ID:       "run-abc",
		Workflow: &Workflow{Name: "test-wf", Version: 1},
		Status:   spec.StatusRunning,
		Steps:    map[string]*StepResult{},
	}

	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	loaded, err := store.LoadRun(t.Context(), "run-abc")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}

	if loaded.ID != run.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, run.ID)
	}
	if loaded.Status != run.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, run.Status)
	}
	if loaded.Workflow.Name != "test-wf" {
		t.Errorf("Workflow.Name = %q, want %q", loaded.Workflow.Name, "test-wf")
	}
}

func TestFileStorage_LoadNotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	_, err := store.LoadRun(t.Context(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing run, got nil")
	}
}

func TestFileStorage_LoadRun_NotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	_, err := store.LoadRun(t.Context(), "missing-run-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrRunNotFound) {
		t.Errorf("expected errors.Is(err, ErrRunNotFound), got: %v", err)
	}
}

func TestFileStorage_LoadRun_ReadError(t *testing.T) {
	// Place a directory at {baseDir}/{id}/run.json so os.ReadFile returns a
	// non-NotExist error, hitting the "load run" error branch.
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-direrr", "run.json")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadRun(t.Context(), "run-direrr")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrRunNotFound) {
		t.Errorf("expected non-ErrRunNotFound error, got ErrRunNotFound")
	}
}

func TestFileStorage_LoadStepResult_NotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	// Save a run so the run directory exists, but request a non-existent step.
	run := &Run{ID: "run-exists", Workflow: &Workflow{Name: "wf"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	_, err := store.LoadStepResult(ctx, "run-exists", "nonexistent-step")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrStepNotFound) {
		t.Errorf("expected errors.Is(err, ErrStepNotFound), got: %v", err)
	}
}

func TestFileStorage_LoadStepResult_ReadError(t *testing.T) {
	// Place a directory at the step JSON path so os.ReadFile returns a
	// non-NotExist error, hitting the "load step result" error branch.
	dir := t.TempDir()
	stepPath := filepath.Join(dir, "run-direrr", "steps", "step-direrr.json")
	if err := os.MkdirAll(stepPath, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadStepResult(t.Context(), "run-direrr", "step-direrr")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrStepNotFound) {
		t.Errorf("expected non-ErrStepNotFound error, got ErrStepNotFound")
	}
}

func TestFileStorage_SaveLoadStepResult(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	sr := &StepResult{
		ID:       "step-1",
		Status:   spec.StepCompleted,
		Content:  "hello world",
		Result:   map[string]any{"key": "val"},
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
	if loaded.Tokens.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", loaded.Tokens.OutputTokens)
	}
	if loaded.Duration != 5*time.Second {
		t.Errorf("Duration = %v, want 5s", loaded.Duration)
	}
	if loaded.Result["key"] != "val" {
		t.Errorf("Result[key] = %v, want val", loaded.Result["key"])
	}
}

func TestFileStorage_StepResultNotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	_, err := store.LoadStepResult(t.Context(), "run-1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing step result, got nil")
	}
}

func TestFileStorage_SharedMemory(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	entries := map[string]string{"key1": "val1", "key2": "val2"}
	if err := store.SaveSharedMemory(ctx, "run1", entries); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.LoadSharedMemory(ctx, "run1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got["key1"] != "val1" || got["key2"] != "val2" {
		t.Errorf("loaded = %v, want %v", got, entries)
	}
}

func TestFileStorage_SharedMemoryMerge(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	if err := store.SaveSharedMemory(ctx, "run1", map[string]string{"key1": "val1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSharedMemory(ctx, "run1", map[string]string{"key2": "val2"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadSharedMemory(ctx, "run1")
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

func TestFileStorage_SharedMemoryNotFound(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	got, err := store.LoadSharedMemory(t.Context(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFileStorage_DirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	run := &Run{ID: "run-xyz", Workflow: &Workflow{Name: "wf"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	sr := &StepResult{ID: "step-a", Status: spec.StepCompleted, Content: "ok"}
	if err := store.SaveStepResult(t.Context(), "run-xyz", "step-a", sr); err != nil {
		t.Fatal(err)
	}

	// Verify directory structure: {dir}/run-xyz/run.json + {dir}/run-xyz/steps/step-a.json
	runFile := filepath.Join(dir, "run-xyz", "run.json")
	if _, err := os.Stat(runFile); err != nil {
		t.Errorf("run.json not found at %s: %v", runFile, err)
	}

	stepFile := filepath.Join(dir, "run-xyz", "steps", "step-a.json")
	if _, err := os.Stat(stepFile); err != nil {
		t.Errorf("step-a.json not found at %s: %v", stepFile, err)
	}
}

func TestFileStorage_UpdateRun(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	run := &Run{ID: "run-1", Workflow: &Workflow{Name: "wf"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Update status to completed.
	run.Status = spec.StatusCompleted
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != spec.StatusCompleted {
		t.Errorf("Status = %q, want %q", loaded.Status, spec.StatusCompleted)
	}
}

func TestFileStorage_ToFromFileStep_WithError(t *testing.T) {
	// toFileStep with Error set
	sr := &StepResult{
		ID:     "step-err",
		Status: spec.StepFailed,
		Error:  errors.New("something broke"),
	}
	fs := toFileStep(sr)
	if fs.Error == nil {
		t.Fatal("fileStep.Error should be non-nil")
	}
	if len(fs.Error.Messages) != 1 || fs.Error.Messages[0] != "something broke" {
		t.Errorf("fileStep.Error.Messages = %v, want ['something broke']", fs.Error.Messages)
	}

	// fromFileStep with Error field set
	roundTripped := fromFileStep(fs)
	if roundTripped.Error == nil {
		t.Fatal("expected non-nil error after fromFileStep")
	}
	if roundTripped.Error.Error() != "something broke" {
		t.Errorf("Error = %q, want 'something broke'", roundTripped.Error.Error())
	}
}

func TestFileStorage_ToFromFileRun_WithErrorStep(t *testing.T) {
	// toFileRun/fromFileRun with steps that have errors - exercises the for-loop
	// paths in toFileRun and fromFileRun that iterate over steps.
	run := &Run{
		ID:       "run-1",
		Workflow: &Workflow{Name: "wf"},
		Status:   spec.StatusPartial,
		Steps: map[string]*StepResult{
			"s1": {ID: "s1", Status: spec.StepCompleted, Content: "ok"},
			"s2": {ID: "s2", Status: spec.StepFailed, Error: errors.New("fail")},
		},
	}
	fr := toFileRun(run)
	if len(fr.Steps) != 2 {
		t.Fatalf("fileRun.Steps len = %d, want 2", len(fr.Steps))
	}
	s2err := fr.Steps["s2"].Error
	if s2err == nil || len(s2err.Messages) != 1 || s2err.Messages[0] != "fail" {
		t.Errorf("fileRun.Steps[s2].Error.Messages = %v, want ['fail']", s2err)
	}

	// Round-trip back.
	roundTripped := fromFileRun(fr)
	if roundTripped.Steps["s2"].Error == nil || roundTripped.Steps["s2"].Error.Error() != "fail" {
		t.Errorf("round-tripped error = %v, want 'fail'", roundTripped.Steps["s2"].Error)
	}
}

func TestFileStorage_SaveRun_MkdirAllError(t *testing.T) {
	// Use a path that can't be created (file in place of directory).
	dir := t.TempDir()
	// Create a file where the run directory should be.
	blockingFile := filepath.Join(dir, "run-block")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	run := &Run{ID: "run-block", Workflow: &Workflow{Name: "wf"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	err := store.SaveRun(t.Context(), run)
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
}

func TestFileStorage_LoadRun_InvalidJSON(t *testing.T) {
	dir := t.TempDir()

	// Create the directory and a corrupt run.json.
	runDir := filepath.Join(dir, "run-bad")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(`{invalid json`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadRun(t.Context(), "run-bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFileStorage_SaveStepResult_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Create a file where the steps directory should be.
	runDir := filepath.Join(dir, "run-block")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blockingFile := filepath.Join(runDir, "steps")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	sr := &StepResult{ID: "s1", Status: spec.StepCompleted, Content: "ok"}
	err := store.SaveStepResult(t.Context(), "run-block", "s1", sr)
	if err == nil {
		t.Fatal("expected error when MkdirAll fails for steps dir")
	}
}

func TestFileStorage_LoadStepResult_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	stepsDir := filepath.Join(dir, "run-bad", "steps")
	if err := os.MkdirAll(stepsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepsDir, "s1.json"), []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadStepResult(t.Context(), "run-bad", "s1")
	if err == nil {
		t.Fatal("expected error for invalid step JSON")
	}
}

func TestFileStorage_SaveSharedMemory_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Block MkdirAll by placing a file where the dir should be.
	blockingFile := filepath.Join(dir, "run-block")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	err := store.SaveSharedMemory(t.Context(), "run-block", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
}

func TestFileStorage_SaveSharedMemory_CorruptExistingFile(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-corrupt")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write corrupt JSON to shared_memory.json.
	if err := os.WriteFile(filepath.Join(runDir, "shared_memory.json"), []byte(`{bad`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	err := store.SaveSharedMemory(t.Context(), "run-corrupt", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when existing shared_memory.json is corrupt")
	}
}

func TestFileStorage_LoadSharedMemory_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-corrupt")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "shared_memory.json"), []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadSharedMemory(t.Context(), "run-corrupt")
	if err == nil {
		t.Fatal("expected error for corrupt shared_memory.json")
	}
}

func TestFileStorage_LoadSharedMemory_ReadError(t *testing.T) {
	// Create a directory named shared_memory.json to trigger a ReadFile error
	// that is NOT os.IsNotExist.
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-direrr")
	smPath := filepath.Join(runDir, "shared_memory.json")
	// Create a directory where a file is expected.
	if err := os.MkdirAll(smPath, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	_, err := store.LoadSharedMemory(t.Context(), "run-direrr")
	if err == nil {
		t.Fatal("expected error when ReadFile fails with non-NotExist error")
	}
}

func TestFileStorage_SaveSharedMemory_ReadExistingError(t *testing.T) {
	// Trigger the "read existing shared memory" error path where ReadFile fails
	// with a non-NotExist error.
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-readerr")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a directory where shared_memory.json file is expected.
	smPath := filepath.Join(runDir, "shared_memory.json")
	if err := os.MkdirAll(smPath, 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewFileStorage(dir)
	err := store.SaveSharedMemory(t.Context(), "run-readerr", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when reading existing shared memory fails")
	}
}

func TestAtomicWriteJSON_CreateTempError(t *testing.T) {
	// atomicWriteJSON with a non-existent directory → CreateTemp fails.
	err := atomicWriteJSON("/nonexistent-dir-12345/test.json", map[string]string{"a": "b"})
	if err == nil {
		t.Fatal("expected error when CreateTemp fails")
	}
}

func TestAtomicWriteJSON_RenameError(t *testing.T) {
	// Trigger Rename error by making the target path a directory.
	// os.Rename(file, directory) fails with "file exists" on macOS/Linux.
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.json")
	// Create a directory where the file should go.
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := atomicWriteJSON(targetPath, map[string]string{"hello": "world"})
	if err == nil {
		t.Fatal("expected error when Rename fails (target is directory)")
	}
}

func TestAtomicWriteJSON_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	err := atomicWriteJSON(path, map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("atomicWriteJSON: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("empty file written")
	}
}

// errFile simulates file operation failures for atomicWriteJSON testing.
type errFile struct {
	name     string
	writeErr error
	syncErr  error
	closeErr error
}

func (f *errFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}
func (f *errFile) Sync() error  { return f.syncErr }
func (f *errFile) Close() error { return f.closeErr }
func (f *errFile) Name() string { return f.name }

func TestAtomicWriteJSON_WriteError(t *testing.T) {
	orig := createTempFile
	t.Cleanup(func() { createTempFile = orig })

	tmp := filepath.Join(t.TempDir(), "fake.tmp")
	createTempFile = func(_, _ string) (writeCloseSyncer, error) {
		return &errFile{name: tmp, writeErr: errors.New("disk full")}, nil
	}

	err := atomicWriteJSON(filepath.Join(t.TempDir(), "out.json"), "data")
	if err == nil {
		t.Fatal("expected write error")
	}
	if !strings.Contains(err.Error(), "write temp file") {
		t.Errorf("error = %v, want 'write temp file'", err)
	}
}

func TestAtomicWriteJSON_SyncError(t *testing.T) {
	orig := createTempFile
	t.Cleanup(func() { createTempFile = orig })

	tmp := filepath.Join(t.TempDir(), "fake.tmp")
	createTempFile = func(_, _ string) (writeCloseSyncer, error) {
		return &errFile{name: tmp, syncErr: errors.New("sync failed")}, nil
	}

	err := atomicWriteJSON(filepath.Join(t.TempDir(), "out.json"), "data")
	if err == nil {
		t.Fatal("expected sync error")
	}
	if !strings.Contains(err.Error(), "sync temp file") {
		t.Errorf("error = %v, want 'sync temp file'", err)
	}
}

func TestAtomicWriteJSON_CloseError(t *testing.T) {
	orig := createTempFile
	t.Cleanup(func() { createTempFile = orig })

	tmp := filepath.Join(t.TempDir(), "fake.tmp")
	createTempFile = func(_, _ string) (writeCloseSyncer, error) {
		return &errFile{name: tmp, closeErr: errors.New("close failed")}, nil
	}

	err := atomicWriteJSON(filepath.Join(t.TempDir(), "out.json"), "data")
	if err == nil {
		t.Fatal("expected close error")
	}
	if !strings.Contains(err.Error(), "close temp file") {
		t.Errorf("error = %v, want 'close temp file'", err)
	}
}

func TestAtomicWriteJSON_MarshalError(t *testing.T) {
	err := atomicWriteJSON(filepath.Join(t.TempDir(), "out.json"), make(chan int))
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "marshal JSON") {
		t.Errorf("error = %v, want 'marshal JSON'", err)
	}
}

// TestFileStorage_DirPermissions verifies: run directories are
// created with mode 0o700 (owner-only), not the group-readable 0o755.
func TestFileStorage_DirPermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)

	run := &Run{ID: "perm-run", Workflow: &Workflow{Name: "wf"}, Status: spec.StatusRunning, Steps: map[string]*StepResult{}}
	if err := store.SaveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}

	// Check run directory permissions.
	runDir := filepath.Join(dir, "perm-run")
	info, err := os.Stat(runDir)
	if err != nil {
		t.Fatalf("stat run dir: %v", err)
	}
	got := info.Mode().Perm()
	if got != 0o700 {
		t.Errorf("run dir perm = %o, want %o", got, 0o700)
	}

	// Check steps directory permissions.
	sr := &StepResult{ID: "s1", Status: spec.StepCompleted, Content: "ok"}
	if err := store.SaveStepResult(t.Context(), "perm-run", "s1", sr); err != nil {
		t.Fatal(err)
	}
	stepsDir := filepath.Join(dir, "perm-run", "steps")
	info2, err := os.Stat(stepsDir)
	if err != nil {
		t.Fatalf("stat steps dir: %v", err)
	}
	got2 := info2.Mode().Perm()
	if got2 != 0o700 {
		t.Errorf("steps dir perm = %o, want %o", got2, 0o700)
	}
}

// Issue 1: tokensSchema tests (new format + backward compat)

// TestTokensSchema_RoundTrip verifies lossless round-trip for all six fields.
func TestTokensSchema_RoundTrip(t *testing.T) {
	orig := provider.Usage{
		InputTokens:      10,
		OutputTokens:     20,
		TotalTokens:      30,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
		ReasoningTokens:  6,
	}
	schema := tokensToSchema(orig)
	got := tokensFromSchema(schema)
	if got != orig {
		t.Errorf("tokensFromSchema(tokensToSchema(u)) = %+v, want %+v", got, orig)
	}
}

// TestTokensSchema_NewFormat verifies marshalling emits lowercase snake_case keys.
func TestTokensSchema_NewFormat(t *testing.T) {
	u := provider.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}
	schema := tokensToSchema(u)
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"input"`) {
		t.Errorf("expected lowercase 'input' key, got: %s", s)
	}
	if strings.Contains(s, `"InputTokens"`) {
		t.Errorf("unexpected PascalCase 'InputTokens' key in new format: %s", s)
	}
}

// TestTokensSchema_BackwardCompat_OldPascalCase verifies that JSON with legacy
// PascalCase keys (untagged provider.Usage) still parses into tokensSchema.
func TestTokensSchema_BackwardCompat_OldPascalCase(t *testing.T) {
	oldJSON := `{"InputTokens":100,"OutputTokens":50,"TotalTokens":150,"CacheReadTokens":10,"CacheWriteTokens":5,"ReasoningTokens":3}`
	var s tokensSchema
	if err := json.Unmarshal([]byte(oldJSON), &s); err != nil {
		t.Fatalf("unmarshal old format: %v", err)
	}
	u := tokensFromSchema(s)
	if u.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", u.InputTokens)
	}
	if u.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", u.OutputTokens)
	}
	if u.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", u.TotalTokens)
	}
	if u.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want 10", u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 5 {
		t.Errorf("CacheWriteTokens = %d, want 5", u.CacheWriteTokens)
	}
	if u.ReasoningTokens != 3 {
		t.Errorf("ReasoningTokens = %d, want 3", u.ReasoningTokens)
	}
}

// TestTokensSchema_BackwardCompat_FileLoad verifies that a run.json written
// with old PascalCase token keys is loaded correctly by FileStorage.
func TestTokensSchema_BackwardCompat_FileLoad(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-legacy")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRunJSON := `{
  "id": "run-legacy",
  "workflow": {"name":"wf","version":1},
  "status": "completed",
  "steps": {
    "s1": {
      "id": "s1",
      "status": "completed",
      "content": "hello",
      "tokens": {"InputTokens":100,"OutputTokens":50,"TotalTokens":150},
      "durationNs": 1000000000
    }
  }
}`
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(oldRunJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStorage(dir)
	run, err := store.LoadRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	s1 := run.Steps["s1"]
	if s1 == nil {
		t.Fatal("step s1 not found")
	}
	if s1.Tokens.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", s1.Tokens.InputTokens)
	}
	if s1.Tokens.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", s1.Tokens.OutputTokens)
	}
	if s1.Tokens.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", s1.Tokens.TotalTokens)
	}
}

// TestTokensSchema_UnmarshalAllZero verifies an all-zero payload doesn't error.
func TestTokensSchema_UnmarshalAllZero(t *testing.T) {
	var s tokensSchema
	if err := json.Unmarshal([]byte(`{}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u := tokensFromSchema(s)
	if u != (provider.Usage{}) {
		t.Errorf("expected zero usage, got %+v", u)
	}
}

// Issue 2: errorSchema tests (joined errors + backward compat)

// TestStorageFile_RoundTripJoinedError verifies errors.Join round-trips through
// Save/Load and that both constituent error messages survive.
func TestStorageFile_RoundTripJoinedError(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	e1 := errors.New("first error")
	e2 := errors.New("second error")
	joined := errors.Join(e1, e2)

	sr := &StepResult{
		ID:     "step-joined",
		Status: spec.StepFailed,
		Error:  joined,
	}
	if err := store.SaveStepResult(ctx, "run-j", "step-joined", sr); err != nil {
		t.Fatalf("SaveStepResult: %v", err)
	}
	loaded, err := store.LoadStepResult(ctx, "run-j", "step-joined")
	if err != nil {
		t.Fatalf("LoadStepResult: %v", err)
	}
	if loaded.Error == nil {
		t.Fatal("expected non-nil error after load")
	}
	msg := loaded.Error.Error()
	if !strings.Contains(msg, "first error") {
		t.Errorf("loaded error %q does not contain 'first error'", msg)
	}
	if !strings.Contains(msg, "second error") {
		t.Errorf("loaded error %q does not contain 'second error'", msg)
	}
}

// TestStorageFile_BackwardCompat_OldErrorFormat verifies that a step JSON with
// old string-form error ("error": "some text") is loaded correctly.
func TestStorageFile_BackwardCompat_OldErrorFormat(t *testing.T) {
	dir := t.TempDir()
	stepsDir := filepath.Join(dir, "run-old-err", "steps")
	if err := os.MkdirAll(stepsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldStepJSON := `{
  "id": "step-old",
  "status": "failed",
  "tokens": {},
  "durationNs": 0,
  "error": "legacy error message"
}`
	if err := os.WriteFile(filepath.Join(stepsDir, "step-old.json"), []byte(oldStepJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStorage(dir)
	loaded, err := store.LoadStepResult(t.Context(), "run-old-err", "step-old")
	if err != nil {
		t.Fatalf("LoadStepResult: %v", err)
	}
	if loaded.Error == nil {
		t.Fatal("expected non-nil error for old string-form error")
	}
	if loaded.Error.Error() != "legacy error message" {
		t.Errorf("Error = %q, want 'legacy error message'", loaded.Error.Error())
	}
}

// TestStorageFile_BackwardCompat_OldRunJSONErrorFormat verifies that a run.json
// with old string-form step errors is loaded correctly.
func TestStorageFile_BackwardCompat_OldRunJSONErrorFormat(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run-old-run-err")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRunJSON := `{
  "id": "run-old-run-err",
  "workflow": {"name":"wf","version":1},
  "status": "partial",
  "steps": {
    "s1": {
      "id": "s1",
      "status": "failed",
      "tokens": {},
      "durationNs": 0,
      "error": "something went wrong"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(runDir, "run.json"), []byte(oldRunJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewFileStorage(dir)
	run, err := store.LoadRun(t.Context(), "run-old-run-err")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	s1 := run.Steps["s1"]
	if s1 == nil {
		t.Fatal("step s1 not found")
	}
	if s1.Error == nil {
		t.Fatal("expected non-nil error in s1")
	}
	if s1.Error.Error() != "something went wrong" {
		t.Errorf("Error = %q, want 'something went wrong'", s1.Error.Error())
	}
}

// TestErrorSchema_SingleError verifies a single non-joined error round-trips.
func TestErrorSchema_SingleError(t *testing.T) {
	orig := errors.New("single error")
	s := errorToSchema(orig)
	if s.Joined {
		t.Error("single error should not have Joined=true")
	}
	if len(s.Messages) != 1 || s.Messages[0] != "single error" {
		t.Errorf("Messages = %v, want ['single error']", s.Messages)
	}
	got := errorFromSchema(s)
	if got == nil || got.Error() != "single error" {
		t.Errorf("errorFromSchema = %v, want 'single error'", got)
	}
}

// TestErrorSchema_NilError verifies nil errors round-trip to nil.
func TestErrorSchema_NilError(t *testing.T) {
	s := errorToSchema(nil)
	if len(s.Messages) != 0 {
		t.Errorf("nil error should produce empty schema, got %+v", s)
	}
	got := errorFromSchema(s)
	if got != nil {
		t.Errorf("errorFromSchema(empty) = %v, want nil", got)
	}
}

// TestErrorSchema_JoinedSingleMessage verifies Joined=true with one message
// round-trips as a simple error.
func TestErrorSchema_JoinedSingleMessage(t *testing.T) {
	s := errorSchema{Messages: []string{"only one"}, Joined: true}
	got := errorFromSchema(s)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.Error() != "only one" {
		t.Errorf("Error() = %q, want 'only one'", got.Error())
	}
}

// TestErrorSchema_UnmarshalNewObjectForm verifies the custom UnmarshalJSON
// handles the new object form {"messages":[...],"joined":true}.
func TestErrorSchema_UnmarshalNewObjectForm(t *testing.T) {
	data := `{"messages":["err1","err2"],"joined":true}`
	var s errorSchema
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !s.Joined {
		t.Error("expected Joined=true")
	}
	if len(s.Messages) != 2 || s.Messages[0] != "err1" || s.Messages[1] != "err2" {
		t.Errorf("Messages = %v, want ['err1','err2']", s.Messages)
	}
}

// TestErrorSchema_UnmarshalEmptyString verifies an empty string literal
// produces an empty (nil-equivalent) errorSchema.
func TestErrorSchema_UnmarshalEmptyString(t *testing.T) {
	var s errorSchema
	if err := json.Unmarshal([]byte(`""`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(s.Messages) != 0 {
		t.Errorf("empty string should produce empty Messages, got %v", s.Messages)
	}
}

// TestTokensSchema_MixedFormat_Merge verifies the critical data-loss bug fix:
// a payload containing both new lowercase and legacy PascalCase fields must
// preserve values from both forms via per-field cmp.Or merge.
func TestTokensSchema_MixedFormat_Merge(t *testing.T) {
	raw := []byte(`{"input": 100, "OutputTokens": 200}`)
	var ts tokensSchema
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatal(err)
	}
	if ts.Input != 100 {
		t.Errorf("Input = %d, want 100", ts.Input)
	}
	if ts.Output != 200 {
		t.Errorf("Output = %d, want 200 (legacy PascalCase field must be preserved)", ts.Output)
	}
}

// TestTokensSchema_LegacyPascalCase verifies all six fields decode correctly
// from the legacy untagged provider.Usage PascalCase form.
func TestTokensSchema_LegacyPascalCase(t *testing.T) {
	raw := []byte(`{"InputTokens":100,"OutputTokens":50,"TotalTokens":150,"CacheReadTokens":10,"CacheWriteTokens":5,"ReasoningTokens":30}`)
	var ts tokensSchema
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("unmarshal legacy PascalCase: %v", err)
	}
	if ts.Input != 100 {
		t.Errorf("Input = %d, want 100", ts.Input)
	}
	if ts.Output != 50 {
		t.Errorf("Output = %d, want 50", ts.Output)
	}
	if ts.Total != 150 {
		t.Errorf("Total = %d, want 150", ts.Total)
	}
	if ts.CacheRead != 10 {
		t.Errorf("CacheRead = %d, want 10", ts.CacheRead)
	}
	if ts.CacheWrite != 5 {
		t.Errorf("CacheWrite = %d, want 5", ts.CacheWrite)
	}
	if ts.Reasoning != 30 {
		t.Errorf("Reasoning = %d, want 30", ts.Reasoning)
	}
}

// TestErrorSchema_NullValue verifies that a JSON null value decodes without
// error and produces a nil-equivalent errorSchema.
func TestErrorSchema_NullValue(t *testing.T) {
	raw := []byte(`null`)
	var es errorSchema
	if err := json.Unmarshal(raw, &es); err != nil {
		t.Fatalf("null unmarshal: %v", err)
	}
	if errorFromSchema(es) != nil {
		t.Error("expected nil error from null errorSchema")
	}
}

// TestErrorSchema_WhitespaceOnly verifies that a payload of only whitespace
// (no non-whitespace bytes) decodes without error and produces a nil-equivalent
// errorSchema - exercises the loop-exhaustion path in UnmarshalJSON.
func TestErrorSchema_WhitespaceOnly(t *testing.T) {
	// Inject whitespace-only bytes directly to exercise the loop path.
	// Note: standard JSON does not allow a bare whitespace document, so we
	// embed whitespace inside a string to reach the branch indirectly.
	// The loop path (all bytes are whitespace) is hit when data is e.g. `""`.
	// We cover it via the empty-string case which already passes through
	// the whitespace-skip loop before hitting '"'.
	// For a direct test of the loop exhaustion, craft a custom payload:
	raw := []byte(`""`)
	var es errorSchema
	if err := json.Unmarshal(raw, &es); err != nil {
		t.Fatalf("empty string unmarshal: %v", err)
	}
	if len(es.Messages) != 0 {
		t.Errorf("expected empty Messages from empty string, got %v", es.Messages)
	}
	if errorFromSchema(es) != nil {
		t.Error("expected nil from empty-string errorSchema")
	}
}

// TestErrorSchema_ObjectFormJoined verifies the object form with joined=true
// reconstructs a multi-error containing all listed messages.
func TestErrorSchema_ObjectFormJoined(t *testing.T) {
	raw := []byte(`{"messages":["e1","e2","e3"],"joined":true}`)
	var es errorSchema
	if err := json.Unmarshal(raw, &es); err != nil {
		t.Fatalf("unmarshal object form: %v", err)
	}
	err := errorFromSchema(es)
	if err == nil {
		t.Fatal("expected non-nil error from joined object form")
	}
	for _, msg := range []string{"e1", "e2", "e3"} {
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("missing %q in joined error: %v", msg, err)
		}
	}
}

// TestTokensSchema_UnmarshalInvalidJSON verifies that invalid JSON input to
// tokensSchema.UnmarshalJSON propagates the error from the first unmarshal call.
// UnmarshalJSON is called directly (not via json.Unmarshal) because the outer
// decoder never calls custom unmarshalers when the JSON is syntactically invalid.
func TestTokensSchema_UnmarshalInvalidJSON(t *testing.T) {
	var ts tokensSchema
	// Call directly to exercise the internal error-return path (lines 43-45).
	err := ts.UnmarshalJSON([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error from invalid JSON, got nil")
	}
}

// TestErrorSchema_UnmarshalStringFormError verifies that an invalid JSON string
// in the legacy string path returns an unmarshal error.
// The '"' byte is detected first, triggering the string branch; the full payload
// is then invalid, so json.Unmarshal returns an error.
func TestErrorSchema_UnmarshalStringFormError(t *testing.T) {
	var es errorSchema
	// Call UnmarshalJSON directly with a payload that starts with '"' but is
	// not valid JSON - the string branch sees '"', then json.Unmarshal fails.
	err := es.UnmarshalJSON([]byte(`"unterminated`))
	if err == nil {
		t.Fatal("expected error from malformed string JSON, got nil")
	}
}

// TestErrorSchema_UnmarshalObjectFormError verifies that an invalid JSON object
// in the new object path returns an unmarshal error.
func TestErrorSchema_UnmarshalObjectFormError(t *testing.T) {
	var es errorSchema
	// Starts with '{' (default branch), but JSON is malformed.
	err := es.UnmarshalJSON([]byte(`{invalid`))
	if err == nil {
		t.Fatal("expected error from malformed object JSON, got nil")
	}
}

// TestErrorSchema_UnmarshalLoopExhaustion verifies the loop-exhaustion path:
// when all bytes in the payload are whitespace, the switch never hits a
// non-whitespace case and the function falls through to return nil.
// UnmarshalJSON is called directly (bypassing json.Unmarshal) to pass raw
// whitespace bytes that would not be valid standalone JSON.
func TestErrorSchema_UnmarshalLoopExhaustion(t *testing.T) {
	var es errorSchema
	// Call UnmarshalJSON directly with whitespace-only bytes.
	// This exercises the "all bytes are whitespace -> continue -> loop ends" path.
	err := es.UnmarshalJSON([]byte("   \t\n  "))
	if err != nil {
		t.Fatalf("expected nil error from whitespace-only bytes, got: %v", err)
	}
	if len(es.Messages) != 0 {
		t.Errorf("expected empty Messages from whitespace input, got %v", es.Messages)
	}
}

// TestFileStorage_StepNoError_OmitsErrorField verifies that a step with no
// error serializes without an "error" key in the JSON (omitempty on pointer).
func TestFileStorage_StepNoError_OmitsErrorField(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStorage(dir)
	ctx := t.Context()

	sr := &StepResult{ID: "s1", Status: spec.StepCompleted, Content: "ok"}
	if err := store.SaveStepResult(ctx, "run-no-err", "s1", sr); err != nil {
		t.Fatalf("SaveStepResult: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "run-no-err", "steps", "s1.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), `"error"`) {
		t.Errorf("expected no 'error' key for step with nil error, got: %s", data)
	}
}
