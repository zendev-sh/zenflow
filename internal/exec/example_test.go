package exec_test

// example_test.go - godoc Example functions for the zenflow package.
// Rules:
// - Functions with an // Output: comment are executed and verified by
// `go test`. All others are compiled but not run (no Output: block).
// - LLM-dependent paths use a fake provider.LanguageModel so examples
// remain deterministic and require no network access.
// - Examples that need no output verification omit the Output: block
// entirely - godoc renders the function body as the usage snippet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
)

// ---- Shared fake LLM used by examples that need a provider ----

// fakeModel is a minimal provider.LanguageModel that returns a canned
// response immediately. Used by examples so they compile and run
// without real API keys.
type fakeModel struct{ reply string }

func (m *fakeModel) ModelID() string { return "example-fake" }

func (m *fakeModel) DoGenerate(_ context.Context, _ provider.GenerateParams) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{
		Text:         m.reply,
		FinishReason: provider.FinishStop,
		Usage:        provider.Usage{InputTokens: 5, OutputTokens: 5},
	}, nil
}

func (m *fakeModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, errors.New("streaming not implemented in fakeModel")
}

// Example demonstrates the minimal embedding pattern: build an
// Orchestrator, load (or construct) a Workflow, and run it.
func Example() {
	ctx := context.Background()

	// Construct an orchestrator backed by a fake LLM.
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "done"}),
	)
	defer orch.Close() //nolint:errcheck

	// Build a one-step workflow programmatically.
	wf := &zenflow.Workflow{
		Name: "hello-world",
		Steps: []zenflow.Step{
			{ID: "greet", Instructions: "Say hello to the user."},
		},
	}

	result, err := orch.RunFlow(ctx, wf)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Status)
	// Output:
	// completed
}

// ExampleNew demonstrates constructing an Orchestrator with the most
// common options. No Output: block - godoc renders the snippet; go test
// compiles it without running.
func ExampleNew() {
	model := &fakeModel{reply: "ok"}

	orch := zenflow.New(
		zenflow.WithModel(model),
		zenflow.WithMaxConcurrency(4),
		zenflow.WithMaxTurns(20),
		zenflow.WithStorage(zenflow.NewMemoryStorage()),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleOrchestrator_RunFlow shows a minimal RunFlow invocation. The
// workflow is built programmatically; callers normally use LoadWorkflow
// to read from a YAML file.
func ExampleOrchestrator_RunFlow() {
	orch := zenflow.New(zenflow.WithModel(&fakeModel{reply: "analysis complete"}))
	defer orch.Close() //nolint:errcheck

	wf := &zenflow.Workflow{
		Name: "analysis",
		Steps: []zenflow.Step{
			{ID: "analyze", Instructions: "Analyze the data."},
		},
	}

	result, err := orch.RunFlow(context.Background(), wf)
	if err != nil {
		log.Fatal(err)
	}
	// result.Steps["analyze"].Content holds the agent's response.
	_ = result
}

// ExampleOrchestrator_RunGoal shows how to supply a goal string and
// handle ErrEmptyGoal when the caller forgets to provide one.
func ExampleOrchestrator_RunGoal() {
	orch := zenflow.New(zenflow.WithModel(&fakeModel{reply: "done"}))
	defer orch.Close() //nolint:errcheck

	// Empty goal returns ErrEmptyGoal immediately - no LLM call needed.
	_, err := orch.RunGoal(context.Background(), "")
	if errors.Is(err, zenflow.ErrEmptyGoal) {
		fmt.Println("goal must not be empty")
	}
	// Output:
	// goal must not be empty
}

// ExampleWithModel shows setting the default LLM for all steps in a
// workflow. Every step that does not declare a model override uses this.
func ExampleWithModel() {
	model := &fakeModel{reply: "hello"}
	orch := zenflow.New(zenflow.WithModel(model))
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithTools shows wiring custom goai.Tool implementations into
// the orchestrator. Tools declared in workflow YAML (by name) are
// matched against this set at runtime.
func ExampleWithTools() {
	greet := goai.Tool{
		Name:        "greet",
		Description: "Returns a greeting.",
		Execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			return "hello", nil
		},
	}
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithTools(greet),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithStorage shows swapping the default MemoryStorage for a
// FileStorage backend. FileStorage persists run state across process
// restarts, enabling ResumeFlow.
func ExampleWithStorage() {
	// MemoryStorage (default) - suitable for short-lived embeddings.
	memOrch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithStorage(zenflow.NewMemoryStorage()),
	)
	defer memOrch.Close() //nolint:errcheck

	// FileStorage - persists under a temp directory for demo.
	dir, _ := os.MkdirTemp("", "zenflow-example-*")
	defer func() { _ = os.RemoveAll(dir) }()

	fileOrch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithStorage(zenflow.NewFileStorage(dir)),
	)
	defer fileOrch.Close() //nolint:errcheck

	_, _ = memOrch, fileOrch
}

// ExampleWithProgress demonstrates wiring a ProgressSink to observe
// workflow events (step starts, completions, errors, narrations).
func ExampleWithProgress() {
	sink := &loggingSink{}
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "done"}),
		zenflow.WithProgress(sink),
	)
	defer orch.Close() //nolint:errcheck

	wf := &zenflow.Workflow{
		Name:  "observed",
		Steps: []zenflow.Step{{ID: "s1", Instructions: "do work"}},
	}
	_, _ = orch.RunFlow(context.Background(), wf)
}

// loggingSink is a simple ProgressSink that prints each event type.
type loggingSink struct{}

func (s *loggingSink) OnEvent(_ context.Context, e zenflow.Event) {
	fmt.Println("event:", e.Type)
}
func (s *loggingSink) OnOutput(_ context.Context, o zenflow.Output) {
	fmt.Print(o.Delta)
}

// ExampleWithMaxConcurrency shows capping the number of workflow steps
// that may execute in parallel. The default is 5.
func ExampleWithMaxConcurrency() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithMaxConcurrency(8), // allow 8 parallel steps
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithMaxTurns caps the number of conversation turns for single
// agent runs (RunAgent). RunFlow uses per-agent MaxTurns from the YAML.
func ExampleWithMaxTurns() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithMaxTurns(10),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithMaxDepth caps the agent nesting depth for RunAgent.
// RunFlow does not support child-agent spawning and ignores this.
func ExampleWithMaxDepth() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithMaxDepth(3),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithApproval shows wiring an approval gate before RunGoal
// executes the coordinator-generated plan.
func ExampleWithApproval() {
	// autoApprove always approves - replace with an interactive prompt.
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithApproval(autoApprove{}),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// autoApprove is a trivial ApprovalHandler that approves every plan.
type autoApprove struct{}

func (autoApprove) ApprovePlan(_ context.Context, _ *zenflow.Workflow) (bool, error) {
	return true, nil
}

// ExampleWithApprovalTimeout shows combining WithApproval and
// WithApprovalTimeout to bound how long the approval handler may block.
func ExampleWithApprovalTimeout() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithApproval(autoApprove{}),
		zenflow.WithApprovalTimeout(30*time.Second),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithPermissions shows gating tool execution with a custom
// PermissionHandler. The handler receives a PermissionRequest and
// returns (approved, error).
func ExampleWithPermissions() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithPermissions(allowAll{}),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// allowAll is a trivial PermissionHandler that allows all tool calls.
type allowAll struct{}

func (allowAll) RequestPermission(_ context.Context, _ zenflow.PermissionRequest) (bool, error) {
	return true, nil
}

// ExampleWithSharedMemory shows enabling shared memory so agents in the
// same workflow can read and write a common key-value store.
func ExampleWithSharedMemory() {
	sm := zenflow.NewSharedMemory()
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithSharedMemory(sm),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithMailboxStore shows replacing the default in-memory mailbox
// with a caller-supplied implementation (e.g. for multi-process
// workflows backed by sqlite or redis).
func ExampleWithMailboxStore() {
	// The factory is invoked once per RunFlow so each run gets a fresh store.
	factory := func() zenflow.MailboxStore { return zenflow.NewInMemoryMailboxStore() }

	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithMailboxStore(factory),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithMaxMailboxSize bounds the per-step in-memory mailbox queue.
// When the cap is exceeded, the router emits EventMessageDropped instead
// of accepting the message.
func ExampleWithMaxMailboxSize() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithMaxMailboxSize(50),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithExternalInbox pre-registers non-step sender identities on
// the Router at Run start. Useful when an external process sends
// RouterMessages into the coordinator's inbox.
func ExampleWithExternalInbox() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "ok"}),
		zenflow.WithExternalInbox("coordinator", "my-external-sender"),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithStreaming enables token-by-token delivery of agent output
// through ProgressSink.OnOutput. Requires WithProgress to be wired.
func ExampleWithStreaming() {
	orch := zenflow.New(
		zenflow.WithModel(&fakeModel{reply: "streaming reply"}),
		zenflow.WithStreaming(),
		zenflow.WithProgress(&loggingSink{}),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleWithModelResolver shows installing a ModelResolver so the
// resume path can rebuild a provider.LanguageModel from the saved
// transcript's model ID string.
func ExampleWithModelResolver() {
	model := &fakeModel{reply: "ok"}

	resolver := zenflow.ModelResolver(func(modelID string) (provider.LanguageModel, error) {
		// In production, switch on modelID to construct the right provider.
		_ = modelID
		return model, nil
	})

	orch := zenflow.New(
		zenflow.WithModel(model),
		zenflow.WithModelResolver(resolver),
	)
	defer orch.Close() //nolint:errcheck
	_ = orch
}

// ExampleParseWorkflow shows parsing a workflow from raw YAML bytes;
// for multi-error handling on invalid YAML see ExampleParseWorkflow_multiError.
func ExampleParseWorkflow() {
	yaml := `
name: greet
steps:
  - id: hello
    instructions: "Say hello to the user."
`
	wf, err := zenflow.ParseWorkflow([]byte(yaml))
	if err != nil {
		// ParseWorkflow may return a joined error for multi-violation YAML.
		// Unwrap with errors.As to inspect individual *zenflow.ValidationError.
		var ve *zenflow.ValidationError
		if errors.As(err, &ve) {
			fmt.Println("validation error:", ve.Message)
		}
		return
	}

	fmt.Println(wf.Name)
	fmt.Println(len(wf.Steps))
	// Output:
	// greet
	// 1
}

// ExampleParseWorkflow_multiError shows how validation errors are surfaced
// as an errors.Join, allowing callers to inspect every violation via
// errors.As / errors.Is on individual ValidationError types.
func ExampleParseWorkflow_multiError() {
	// Two agents both missing the required 'description' field - ValidateWorkflow
	// accumulates one ValidationError per agent and joins them, so the
	// caller sees all violations in one error value rather than only the first.
	invalid := []byte(`
name: test
agents:
  a:
    prompt: "do a"
  b:
    prompt: "do b"
steps:
  - id: s1
    agent: a
    instructions: "run a"
  - id: s2
    agent: b
    instructions: "run b"
    dependsOn: [s1]
`)
	_, err := zenflow.ParseWorkflow(invalid)
	if err == nil {
		return
	}
	// err is errors.Join'd; iterate sub-errors via Unwrap []error.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		fmt.Printf("found %d validation errors\n", len(joined.Unwrap()))
	}

	// Output:
	// found 2 validation errors
}
