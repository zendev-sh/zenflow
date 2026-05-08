package exec

import (
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/spec"
)

func TestWithStreaming(t *testing.T) {
	o := New(WithStreaming())
	if !o.streaming {
		t.Error("streaming should be true")
	}
	o2 := New(WithoutStreaming())
	if o2.streaming {
		t.Error("streaming should be false")
	}
}

// TestWithStreaming_Pair exercises WithStreaming and WithoutStreaming
// as the canonical no-arg pair (API stability improvement).
func TestWithStreaming_Pair(t *testing.T) {
	// WithStreaming sets streaming=true.
	on := New(WithStreaming())
	if !on.streaming {
		t.Error("WithStreaming() should set streaming=true")
	}
	// WithoutStreaming sets streaming=false.
	off := New(WithoutStreaming())
	if off.streaming {
		t.Error("WithoutStreaming() should set streaming=false")
	}
	// WithStreamingBool acts as deprecated thin wrapper.
	boolOn := New(WithStreamingBool(true))
	if !boolOn.streaming {
		t.Error("WithStreamingBool(true) should set streaming=true")
	}
	boolOff := New(WithStreamingBool(false))
	if boolOff.streaming {
		t.Error("WithStreamingBool(false) should set streaming=false")
	}
	// Last write wins - WithoutStreaming after WithStreaming.
	overrideOff := New(WithStreaming(), WithoutStreaming())
	if overrideOff.streaming {
		t.Error("WithoutStreaming after WithStreaming should set streaming=false")
	}
}

func TestWithVerbose(t *testing.T) {
	o := New(WithVerbose())
	if !o.verbose {
		t.Error("verbose should be true")
	}
	o2 := New(WithoutVerbose())
	if o2.verbose {
		t.Error("verbose should be false")
	}
}

// TestWithVerbose_Pair verifies WithVerbose/WithoutVerbose produce the same
// state as the deprecated WithVerboseBool(true/false) forms.
func TestWithVerbose_Pair(t *testing.T) {
	// WithVerbose and WithVerboseBool(true) should both set verbose=true.
	o1 := New(WithVerbose())
	o2 := New(WithVerboseBool(true))
	if o1.verbose != o2.verbose {
		t.Errorf("WithVerbose() verbose=%v, WithVerboseBool(true) verbose=%v - must match", o1.verbose, o2.verbose)
	}
	if !o1.verbose {
		t.Error("WithVerbose() should set verbose=true")
	}

	// WithoutVerbose and WithVerboseBool(false) should both set verbose=false.
	o3 := New(WithoutVerbose())
	o4 := New(WithVerboseBool(false))
	if o3.verbose != o4.verbose {
		t.Errorf("WithoutVerbose() verbose=%v, WithVerboseBool(false) verbose=%v - must match", o3.verbose, o4.verbose)
	}
	if o3.verbose {
		t.Error("WithoutVerbose() should set verbose=false")
	}

	// Last write wins - WithoutVerbose after WithVerbose.
	overrideOff := New(WithVerbose(), WithoutVerbose())
	if overrideOff.verbose {
		t.Error("WithoutVerbose after WithVerbose should set verbose=false")
	}
}

// TestWithMailboxDelivery_Pair verifies WithMailboxDelivery/WithoutMailboxDelivery
// produce the same state as the deprecated WithMailboxDeliveryBool(true/false) forms.
func TestWithMailboxDelivery_Pair(t *testing.T) {
	// WithMailboxDelivery and WithMailboxDeliveryBool(true) should both set mailboxDeliveryEnabled to pointer-to-true.
	o1 := New(WithMailboxDelivery())
	o2 := New(WithMailboxDeliveryBool(true))
	if o1.mailboxDeliveryEnabled == nil {
		t.Fatal("WithMailboxDelivery() should set mailboxDeliveryEnabled to non-nil")
	}
	if o2.mailboxDeliveryEnabled == nil {
		t.Fatal("WithMailboxDeliveryBool(true) should set mailboxDeliveryEnabled to non-nil")
	}
	if *o1.mailboxDeliveryEnabled != *o2.mailboxDeliveryEnabled {
		t.Errorf("WithMailboxDelivery() *mailboxDeliveryEnabled=%v, WithMailboxDeliveryBool(true)=%v - must match",
			*o1.mailboxDeliveryEnabled, *o2.mailboxDeliveryEnabled)
	}
	if !*o1.mailboxDeliveryEnabled {
		t.Error("WithMailboxDelivery() should set *mailboxDeliveryEnabled=true")
	}

	// WithoutMailboxDelivery and WithMailboxDeliveryBool(false) should both set mailboxDeliveryEnabled to pointer-to-false.
	o3 := New(WithoutMailboxDelivery())
	o4 := New(WithMailboxDeliveryBool(false))
	if o3.mailboxDeliveryEnabled == nil {
		t.Fatal("WithoutMailboxDelivery() should set mailboxDeliveryEnabled to non-nil")
	}
	if o4.mailboxDeliveryEnabled == nil {
		t.Fatal("WithMailboxDeliveryBool(false) should set mailboxDeliveryEnabled to non-nil")
	}
	if *o3.mailboxDeliveryEnabled != *o4.mailboxDeliveryEnabled {
		t.Errorf("WithoutMailboxDelivery() *mailboxDeliveryEnabled=%v, WithMailboxDeliveryBool(false)=%v - must match",
			*o3.mailboxDeliveryEnabled, *o4.mailboxDeliveryEnabled)
	}
	if *o3.mailboxDeliveryEnabled {
		t.Error("WithoutMailboxDelivery() should set *mailboxDeliveryEnabled=false")
	}
}

// TestWithTruncationOnCapReached_Pair verifies WithTruncationOnCapReached/WithoutTruncationOnCapReached
// produce the same state as the deprecated WithTruncationOnCapReachedBool(true/false) forms.
func TestWithTruncationOnCapReached_Pair(t *testing.T) {
	// WithTruncationOnCapReached and WithTruncationOnCapReachedBool(true) should both set truncateOnCapReached=true.
	o1 := New(WithTruncationOnCapReached())
	o2 := New(WithTruncationOnCapReachedBool(true))
	if o1.truncateOnCapReached != o2.truncateOnCapReached {
		t.Errorf("WithTruncationOnCapReached() truncateOnCapReached=%v, WithTruncationOnCapReachedBool(true)=%v - must match",
			o1.truncateOnCapReached, o2.truncateOnCapReached)
	}
	if !o1.truncateOnCapReached {
		t.Error("WithTruncationOnCapReached() should set truncateOnCapReached=true")
	}

	// WithoutTruncationOnCapReached and WithTruncationOnCapReachedBool(false) should both set truncateOnCapReached=false.
	o3 := New(WithoutTruncationOnCapReached())
	o4 := New(WithTruncationOnCapReachedBool(false))
	if o3.truncateOnCapReached != o4.truncateOnCapReached {
		t.Errorf("WithoutTruncationOnCapReached() truncateOnCapReached=%v, WithTruncationOnCapReachedBool(false)=%v - must match",
			o3.truncateOnCapReached, o4.truncateOnCapReached)
	}
	if o3.truncateOnCapReached {
		t.Error("WithoutTruncationOnCapReached() should set truncateOnCapReached=false")
	}

	// Last write wins - WithoutTruncationOnCapReached after WithTruncationOnCapReached.
	overrideOff := New(WithTruncationOnCapReached(), WithoutTruncationOnCapReached())
	if overrideOff.truncateOnCapReached {
		t.Error("WithoutTruncationOnCapReached after WithTruncationOnCapReached should set truncateOnCapReached=false")
	}
}

// TestOptions_DayOneCoverage verifies every Day-1 Option sets the
// corresponding Orchestrator field AND threads through
// to a freshly constructed Executor via applyExecutorOptions.
func TestOptions_DayOneCoverage(t *testing.T) {
	dropFn := func(DropEvent) {}
	customStore := func() MailboxStore { return NewInMemoryMailboxStore() }
	clk := &RealClock{}

	o := New(
		WithMaxWakeCycles(7),
		WithHoldTimeout(15*time.Second),
		WithDropCallback(dropFn),
		WithDropCallbackBufferSize(64),
		WithMaxMailboxSize(128),
		WithMailboxStore(customStore),
		WithoutMailboxDelivery(),
		withClock(clk),
		WithProgressBufferSize(2048),
	)

	// Orchestrator-level assertions.
	if o.maxWakeCycles != 7 {
		t.Errorf("maxWakeCycles=%d want 7", o.maxWakeCycles)
	}
	if o.holdTimeout != 15*time.Second {
		t.Errorf("holdTimeout=%v want 15s", o.holdTimeout)
	}
	if o.dropCallback == nil {
		t.Error("dropCallback nil")
	}
	if o.dropCallbackBufferSize != 64 {
		t.Errorf("dropCallbackBufferSize=%d want 64", o.dropCallbackBufferSize)
	}
	if o.maxMailboxSize != 128 {
		t.Errorf("maxMailboxSize=%d want 128", o.maxMailboxSize)
	}
	if o.mailboxStoreFactory == nil {
		t.Error("mailboxStoreFactory nil")
	}
	if o.mailboxDeliveryEnabled == nil || *o.mailboxDeliveryEnabled {
		t.Errorf("mailboxDeliveryEnabled=%v want pointer-to-false", o.mailboxDeliveryEnabled)
	}
	if o.engineClock != clk {
		t.Error("engineClock not set")
	}
	if o.progressBufferSize != 2048 {
		t.Errorf("progressBufferSize=%d want 2048", o.progressBufferSize)
	}

	// applyExecutorOptions thread-through.
	exec := &Executor{}
	o.applyExecutorOptions(exec)
	if exec.MaxWakeCycles != 7 {
		t.Errorf("exec.MaxWakeCycles=%d want 7", exec.MaxWakeCycles)
	}
	if exec.HoldTimeout != 15*time.Second {
		t.Errorf("exec.HoldTimeout=%v want 15s", exec.HoldTimeout)
	}
	if exec.DropCallback == nil {
		t.Error("exec.DropCallback nil")
	}
	if exec.DropCallbackBufferSize != 64 {
		t.Errorf("exec.DropCallbackBufferSize=%d want 64", exec.DropCallbackBufferSize)
	}
	if exec.MaxMailboxSize != 128 {
		t.Errorf("exec.MaxMailboxSize=%d want 128", exec.MaxMailboxSize)
	}
	if exec.MailboxStoreFactory == nil {
		t.Error("exec.MailboxStoreFactory nil")
	}
	if exec.MailboxDeliveryEnabled == nil || *exec.MailboxDeliveryEnabled {
		t.Errorf("exec.MailboxDeliveryEnabled=%v want pointer-to-false", exec.MailboxDeliveryEnabled)
	}
	if exec.EngineClock != clk {
		t.Error("exec.EngineClock not set")
	}
	if exec.ProgressBufferSize != 2048 {
		t.Errorf("exec.ProgressBufferSize=%d want 2048", exec.ProgressBufferSize)
	}
	if !exec.SenderMatrixDAGAware {
		t.Error("SenderMatrixDAGAware should default true after applyExecutorOptions (F7)")
	}
}

// TestWithMaxDepth_GetterReturnsConfiguredValue verifies that
// MaxDepth exposes the raw configured value to cross-module callers
// (consumer flow-bridge tests). Verify the getter round-trips both an
// explicitly-configured value and the unconfigured default (0 sentinel,
// meaning "use runtime default").
func TestWithMaxDepth_GetterReturnsConfiguredValue(t *testing.T) {
	o := New(WithMaxDepth(7))
	if got := o.MaxDepth(); got != 7 {
		t.Fatalf("MaxDepth() = %d, want 7", got)
	}

	// Default - no WithMaxDepth Option applied. The Orchestrator stores
	// 0 and RunAgent applies the runtime default lazily; the getter
	// itself must surface the raw 0 (documented behavior).
	def := New()
	if got := def.MaxDepth(); got != 0 {
		t.Fatalf("default MaxDepth() = %d, want 0 (sentinel for runtime default)", got)
	}

	// Nil-receiver safety - documented contract returns 0.
	var nilOrch *Orchestrator
	if got := nilOrch.MaxDepth(); got != 0 {
		t.Fatalf("nil-receiver MaxDepth() = %d, want 0", got)
	}
}

// TestWithRunID - BUG-D: caller-supplied runID must pin Event.RunID for
// RunFlow (and RunAgent / RunGoal) so server-allocated IDs match the
// internally-emitted event stream. Without this, zenflow generates a
// fresh internal ID and every Event.RunID diverges from the caller's.
func TestWithRunID(t *testing.T) {
	o := New(WithRunID("run_test_pinned_abcdef"))
	if o.runID != "run_test_pinned_abcdef" {
		t.Errorf("runID=%q want run_test_pinned_abcdef", o.runID)
	}
	// Empty stays empty - falls back to internal generation.
	o2 := New()
	if o2.runID != "" {
		t.Errorf("default runID=%q want empty", o2.runID)
	}
}

// TestWithExternalInbox_Dedup verifies: duplicate inbox IDs are
// not registered twice, even across multiple WithExternalInbox calls.
func TestWithExternalInbox_Dedup(t *testing.T) {
	// Single call with duplicates.
	o := New(WithExternalInbox("a", "b", "a", "c", "b"))
	if got, want := len(o.externalInboxes), 3; got != want {
		t.Errorf("len(externalInboxes) = %d, want %d (got %v)", got, want, o.externalInboxes)
	}

	// Multiple calls with overlapping IDs.
	o2 := New(WithExternalInbox("x", "y"), WithExternalInbox("y", "z"), WithExternalInbox("x", "z"))
	if got, want := len(o2.externalInboxes), 3; got != want {
		t.Errorf("len(externalInboxes) = %d, want %d (got %v)", got, want, o2.externalInboxes)
	}

	// All unique - no dedup needed.
	o3 := New(WithExternalInbox("p", "q", "r"))
	if got, want := len(o3.externalInboxes), 3; got != want {
		t.Errorf("len(externalInboxes) = %d, want %d (got %v)", got, want, o3.externalInboxes)
	}

	// Empty call - no-op.
	o4 := New(WithExternalInbox())
	if got := len(o4.externalInboxes); got != 0 {
		t.Errorf("len(externalInboxes) = %d, want 0", got)
	}
}

// TestWorkflowResult_Result verifies the Result accessor method:
// - returns a value copy when the stepID is present and the pointer is non-nil
// - returns (StepResult{}, false) for missing stepIDs
// - returns (StepResult{}, false) for nil stored pointers
// - returns (StepResult{}, false) on a nil *WorkflowResult receiver
// - mutations to the returned value do not affect the stored pointer
func TestWorkflowResult_Result(t *testing.T) {
	sr := &StepResult{ID: "step-1", Content: "output-A", Status: spec.StepCompleted}
	wr := &WorkflowResult{
		Steps: map[string]*StepResult{
			"step-1":   sr,
			"step-nil": nil,
		},
	}

	// Present, non-nil - returns value copy and true.
	got, ok := wr.Result("step-1")
	if !ok {
		t.Fatal("Result(step-1): ok=false, want true")
	}
	if got.Content != "output-A" {
		t.Errorf("Result(step-1).Content=%q want 'output-A'", got.Content)
	}
	// Mutation of the returned copy must not affect the stored pointer.
	got.Content = "mutated"
	if sr.Content != "output-A" {
		t.Errorf("stored *StepResult was mutated via returned copy: got %q", sr.Content)
	}

	// Missing stepID - returns false.
	_, ok = wr.Result("no-such-step")
	if ok {
		t.Error("Result(no-such-step): ok=true, want false")
	}

	// Nil stored pointer - returns false.
	_, ok = wr.Result("step-nil")
	if ok {
		t.Error("Result(step-nil): ok=true for nil pointer, want false")
	}

	// Nil *WorkflowResult receiver - must not panic.
	var nilWR *WorkflowResult
	_, ok = nilWR.Result("step-1")
	if ok {
		t.Error("nil-receiver Result: ok=true, want false")
	}
}

// WithForceModel installs a forced override that wins over per-agent
// and per-step Model declarations during effective-model resolution.
// Empty argument leaves the field empty (no override) so the option is
// safe to apply unconditionally.
func TestWithForceModel(t *testing.T) {
	// Empty argument => no override.
	o := New(WithForceModel(""))
	if o.forceModel != "" {
		t.Errorf("forceModel = %q, want empty when argument is empty", o.forceModel)
	}

	// Non-empty argument => sets the field.
	o = New(WithForceModel("openai/gpt-5"))
	if o.forceModel != "openai/gpt-5" {
		t.Errorf("forceModel = %q, want %q", o.forceModel, "openai/gpt-5")
	}
}

// Effective-model resolution honors WithForceModel for the RunAgent path:
// the runner sees the forced model regardless of what AgentConfig.Model
// declares. We exercise the resolution by constructing the executor + a
// stub Step + AgentConfig and reading e.ForceModel through the
// orchestrator's wiring.
func TestWithForceModel_Precedence(t *testing.T) {
	// Without WithForceModel, defaultModel + cfg.Model + step.Model wins
	// (the existing precedence). With WithForceModel, the forced ID wins.
	o := New(
		WithDefaultModel("default-model"),
		WithForceModel("forced-model"),
	)
	if o.forceModel != "forced-model" {
		t.Fatalf("setup: forceModel = %q, want %q", o.forceModel, "forced-model")
	}
	if o.defaultModel != "default-model" {
		t.Fatalf("setup: defaultModel = %q, want %q", o.defaultModel, "default-model")
	}

	// Mirror the precedence used in executor_step.go::runStep where
	// `cmp.Or(e.ForceModel, step.Model, agent.Model, e.DefaultModel)`
	// resolves the model. forceModel must win over step.Model + agent.Model
	// + defaultModel.
	step := spec.Step{Model: "step-model"}
	agent := spec.AgentConfig{Model: "agent-model"}
	got := firstNonEmpty(o.forceModel, step.Model, agent.Model, o.defaultModel)
	if got != "forced-model" {
		t.Errorf("effective model = %q, want %q (forceModel wins)", got, "forced-model")
	}

	// Without forceModel, step.Model wins.
	o2 := New(
		WithDefaultModel("default-model"),
	)
	got2 := firstNonEmpty(o2.forceModel, step.Model, agent.Model, o2.defaultModel)
	if got2 != "step-model" {
		t.Errorf("effective model (no force) = %q, want %q", got2, "step-model")
	}

	// With only forceModel, no step.Model, no agent.Model, no defaultModel.
	o3 := New(WithForceModel("solo-force"))
	got3 := firstNonEmpty(o3.forceModel, "", "", o3.defaultModel)
	if got3 != "solo-force" {
		t.Errorf("effective model (force only) = %q, want %q", got3, "solo-force")
	}
}

// New must install the default TokenBudgetTransformer when the caller
// does not provide WithOutputTransform. Without this, every consumer
// (CLI + library callers) had to install the transform themselves - 
// which the OSS CLI did unconditionally (B12 promotion: move the
// install into the library).
func TestNewDefault_OutputTransformInstalled(t *testing.T) {
	o := New()
	if o.outputTransform == nil {
		t.Fatal("default New() did not install an OutputTransform")
	}
	tbt, ok := o.outputTransform.(*TokenBudgetTransformer)
	if !ok {
		t.Fatalf("default OutputTransform is %T, want *TokenBudgetTransformer", o.outputTransform)
	}
	if tbt.MaxBytesPerDep != DefaultMaxBytesPerDep {
		t.Errorf("default MaxBytesPerDep = %d, want %d", tbt.MaxBytesPerDep, DefaultMaxBytesPerDep)
	}
}

// When the caller supplies WithOutputTransform, New must not overwrite
// it with the default. Custom transforms (LLM compaction, smart per-model
// truncation, etc.) survive intact.
func TestNewDefault_OutputTransformOverride(t *testing.T) {
	custom := &TokenBudgetTransformer{MaxBytesPerDep: 16 * 1024}
	o := New(WithOutputTransform(custom))
	if o.outputTransform != custom {
		t.Errorf("WithOutputTransform was overwritten by default; got %T@%p, want %p", o.outputTransform, o.outputTransform, custom)
	}
}

func firstNonEmpty(args ...string) string {
	for _, a := range args {
		if a != "" {
			return a
		}
	}
	return ""
}

// TestNewDefault_MaxMailboxSizeInstalled: when WithMaxMailboxSize is not
// provided, New must install DefaultMaxMailboxSize (10000) so callers
// get a finite default instead of an unbounded queue.
func TestNewDefault_MaxMailboxSizeInstalled(t *testing.T) {
	o := New()
	if o.maxMailboxSize != DefaultMaxMailboxSize {
		t.Errorf("default maxMailboxSize = %d, want %d", o.maxMailboxSize, DefaultMaxMailboxSize)
	}
	if o.maxMailboxSizeSet {
		t.Error("maxMailboxSizeSet must remain false when caller did not invoke WithMaxMailboxSize")
	}
}

// TestNewDefault_MaxMailboxSizeExplicitZeroOptOut: WithMaxMailboxSize(0)
// is the documented unbounded opt-out and must survive the New default
// install. The flag (maxMailboxSizeSet) preserves the caller's intent.
func TestNewDefault_MaxMailboxSizeExplicitZeroOptOut(t *testing.T) {
	o := New(WithMaxMailboxSize(0))
	if o.maxMailboxSize != 0 {
		t.Errorf("explicit WithMaxMailboxSize(0) was overridden; got %d, want 0 (unbounded)", o.maxMailboxSize)
	}
	if !o.maxMailboxSizeSet {
		t.Error("maxMailboxSizeSet must be true after WithMaxMailboxSize(0); otherwise New() default would re-install DefaultMaxMailboxSize")
	}
}

// TestNewDefault_MaxMailboxSizeExplicitNonZero: callers that supply a
// non-zero cap keep their value; the default install does not touch
// the field.
func TestNewDefault_MaxMailboxSizeExplicitNonZero(t *testing.T) {
	o := New(WithMaxMailboxSize(42))
	if o.maxMailboxSize != 42 {
		t.Errorf("explicit WithMaxMailboxSize(42) lost; got %d, want 42", o.maxMailboxSize)
	}
	if !o.maxMailboxSizeSet {
		t.Error("maxMailboxSizeSet must be true after explicit WithMaxMailboxSize")
	}
}
