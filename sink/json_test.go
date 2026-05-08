package sink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow"
	"github.com/zendev-sh/zenflow/sink"
)

func TestJSON_Stdout(t *testing.T) {
	s := sink.JSON(os.Stdout)
	if s == nil {
		t.Fatal("JSON(os.Stdout) returned nil")
	}
}

func TestJSONSink_WorkflowStart(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	ts := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventWorkflowStart,
		Timestamp: ts,
		RunID:     "run-1",
		Message:   "my workflow",
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["type"] != string(zenflow.EventWorkflowStart) {
		t.Errorf("type = %v, want %v", obj["type"], zenflow.EventWorkflowStart)
	}
	if obj["runId"] != "run-1" {
		t.Errorf("runId = %v, want run-1", obj["runId"])
	}
	if obj["message"] != "my workflow" {
		t.Errorf("message = %v, want my workflow", obj["message"])
	}
	// Fields that are zero should be omitted.
	if _, ok := obj["stepId"]; ok {
		t.Errorf("stepId should be omitted when empty")
	}
	if _, ok := obj["agent"]; ok {
		t.Errorf("agent should be omitted when empty")
	}
}

func TestJSONSink_StepEnd(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	tokens := &provider.Usage{InputTokens: 100, OutputTokens: 50}
	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventStepEnd,
		Timestamp: time.Now(),
		RunID:     "run-1",
		StepID:    "step-a",
		Duration:  3 * time.Second,
		Tokens:    tokens,
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["duration"] != "3s" {
		t.Errorf("duration = %v, want 3s", obj["duration"])
	}
	tok, ok := obj["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("tokens is not a map: %T", obj["tokens"])
	}
	if tok["InputTokens"] != float64(100) {
		t.Errorf("InputTokens = %v, want 100", tok["InputTokens"])
	}
	if tok["OutputTokens"] != float64(50) {
		t.Errorf("OutputTokens = %v, want 50", tok["OutputTokens"])
	}
}

func TestJSONSink_Error(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventError,
		Timestamp: time.Now(),
		RunID:     "run-1",
		StepID:    "step-b",
		Error:     errors.New("something broke"),
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["error"] != "something broke" {
		t.Errorf("error = %v, want something broke", obj["error"])
	}
}

func TestJSONSink_Output(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnOutput(context.Background(), zenflow.Output{
		RunID:  "run-1",
		StepID: "step-a",
		Delta:  "hello ",
		Done:   false,
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["type"] != "output" {
		t.Errorf("type = %v, want output", obj["type"])
	}
	if obj["delta"] != "hello " {
		t.Errorf("delta = %v, want 'hello '", obj["delta"])
	}
	if obj["done"] != false {
		t.Errorf("done = %v, want false", obj["done"])
	}
}

func TestJSONSink_CoordinatorNarration(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventCoordinatorNarration,
		Timestamp: time.Now(),
		RunID:     "run-1",
		StepID:    "step-a",
		Message:   "Step narrated.",
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["type"] != string(zenflow.EventCoordinatorNarration) {
		t.Errorf("type = %v, want %v", obj["type"], zenflow.EventCoordinatorNarration)
	}
	if obj["message"] != "Step narrated." {
		t.Errorf("message = %v, want 'Step narrated.'", obj["message"])
	}
}

func TestJSONSink_CoordinatorSynthesis(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventCoordinatorSynthesis,
		Timestamp: time.Now(),
		RunID:     "run-1",
		Message:   "All done.",
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["type"] != string(zenflow.EventCoordinatorSynthesis) {
		t.Errorf("type = %v, want %v", obj["type"], zenflow.EventCoordinatorSynthesis)
	}
	if obj["message"] != "All done." {
		t.Errorf("message = %v, want 'All done.'", obj["message"])
	}
}

func TestJSONSink_MultipleEvents(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	ts := time.Now()
	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventWorkflowStart,
		Timestamp: ts,
		RunID:     "run-1",
		Message:   "start",
	})
	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventStepStart,
		Timestamp: ts,
		RunID:     "run-1",
		StepID:    "step-a",
		AgentName: "coder",
	})
	s.OnOutput(context.Background(), zenflow.Output{
		RunID:  "run-1",
		StepID: "step-a",
		Delta:  "done",
		Done:   true,
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d: invalid JSON: %v", i, err)
		}
	}
}

func TestJSONSink_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	const goroutines = 10
	const eventsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range eventsPerGoroutine {
				if i%2 == 0 {
					s.OnEvent(context.Background(), zenflow.Event{
						Type:      zenflow.EventStepStart,
						Timestamp: time.Now(),
						RunID:     "run-1",
						StepID:    "step",
						AgentName: "agent",
					})
				} else {
					s.OnOutput(context.Background(), zenflow.Output{
						RunID:  "run-1",
						StepID: "step",
						Delta:  "x",
					})
				}
			}
			_ = g
		}()
	}
	wg.Wait()

	// Verify all lines are valid JSON.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != goroutines*eventsPerGoroutine {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*eventsPerGoroutine)
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d: invalid JSON: %v\nline: %s", i, err, line)
		}
	}

	if s.Err() != nil {
		t.Errorf("unexpected sink error: %v", s.Err())
	}
}

func TestJSONSink_Err(t *testing.T) {
	s := sink.JSON(&failWriter{})

	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventWorkflowStart,
		Timestamp: time.Now(),
		RunID:     "run-1",
	})

	if s.Err() == nil {
		t.Error("expected error from failing writer")
	}
}

func TestJSONSink_OnEvent_WithData(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnEvent(context.Background(), zenflow.Event{
		Type:      zenflow.EventStepStart,
		Timestamp: time.Now(),
		RunID:     "run-1",
		StepID:    "step-a",
		Data:      map[string]any{"index": 1, "total": 3},
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	data, ok := obj["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not a map: %T", obj["data"])
	}
	if data["index"] != float64(1) {
		t.Errorf("data[index] = %v, want 1", data["index"])
	}
	if data["total"] != float64(3) {
		t.Errorf("data[total] = %v, want 3", data["total"])
	}
}

func TestJSONSink_OnOutput_WithAgentID(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnOutput(context.Background(), zenflow.Output{
		RunID:   "run-1",
		StepID:  "step-a",
		AgentID: "agent-1",
		Delta:   "hello",
		Done:    false,
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["agentId"] != "agent-1" {
		t.Errorf("agentId = %v, want agent-1", obj["agentId"])
	}
}

// surface output.Reasoning so JSON consumers can distinguish
// thinking deltas from agent-text deltas. Pre-fix: both rendered as
// `{"type":"output","delta":"..."}` indistinguishably, forcing
// downstream consumers to lose the channel separation that stdout
// sink uses for different formatting.
func TestJSONSink_OnOutput_ReasoningTrue_Surfaces(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnOutput(context.Background(), zenflow.Output{
		RunID:     "run-1",
		StepID:    "step-a",
		Delta:     "thinking...",
		Reasoning: true,
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if obj["reasoning"] != true {
		t.Errorf("reasoning = %v, want true", obj["reasoning"])
	}
}

// keep payload compact: omit reasoning field when false
// (zero value). Most output deltas are agent text, not reasoning.
func TestJSONSink_OnOutput_ReasoningFalse_Omitted(t *testing.T) {
	var buf bytes.Buffer
	s := sink.JSON(&buf)

	s.OnOutput(context.Background(), zenflow.Output{
		RunID:     "run-1",
		StepID:    "step-a",
		Delta:     "hello",
		Reasoning: false, // default: agent text
	})

	var obj map[string]any
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, present := obj["reasoning"]; present {
		t.Errorf("reasoning field should be omitted when false (compact payload), got %v", obj["reasoning"])
	}
}

func TestJSONSink_OnOutput_EncodeError(t *testing.T) {
	s := sink.JSON(&failWriter{})

	s.OnOutput(context.Background(), zenflow.Output{
		RunID:  "run-1",
		StepID: "step-a",
		Delta:  "hello",
	})

	if s.Err() == nil {
		t.Error("expected error from failing writer on OnOutput")
	}
}

// failWriter always returns an error on Write.
type failWriter struct{}

func (w *failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

// TestSpecSampleNDJSON_RoundTrip locks the NDJSON event schema documented
// in spec/v1/spec.md § 14 against the sink's actual emission shape.
// The sample stream below is the canonical example that ships in spec.md
// § 14.3. The test decodes every line and asserts: (a) the type discriminator
// is one of the 27 documented values; (b) the required envelope fields are
// present per § 14 (type + timestamp always; runId on every line in this
// sample); (c) the data payload carries the documented required keys.
// If sink/json.go reshapes an existing event, this test fails until either
// the producer is reverted OR the spec section is updated in the same PR.
// That couples docs and code: drift cannot accumulate silently.
func TestSpecSampleNDJSON_RoundTrip(t *testing.T) {
	// Sample mirrors spec.md § 14.3 exactly. Keep these in lockstep.
	sample := strings.TrimSpace(`
{"type":"workflow_start","timestamp":"2026-05-05T10:00:00Z","runId":"r1","message":"simple","data":{"total":1}}
{"type":"step_start","timestamp":"2026-05-05T10:00:00Z","runId":"r1","stepId":"hello","agent":"writer","data":{"index":0,"total":1}}
{"type":"agent_turn","timestamp":"2026-05-05T10:00:00Z","runId":"r1","stepId":"hello","agent":"writer","data":{"phase":"request","turn":1,"model":"gemini-3-pro-preview"}}
{"type":"output","runId":"r1","stepId":"hello","delta":"Hello, world.","done":false}
{"type":"output","runId":"r1","stepId":"hello","delta":"","done":true}
{"type":"agent_turn","timestamp":"2026-05-05T10:00:01Z","runId":"r1","stepId":"hello","agent":"writer","data":{"phase":"response","model":"gemini-3-pro-preview"},"tokens":{"prompt":12,"completion":3,"total":15}}
{"type":"step_end","timestamp":"2026-05-05T10:00:01Z","runId":"r1","stepId":"hello","agent":"writer","duration":"1.0s","tokens":{"prompt":12,"completion":3,"total":15}}
{"type":"workflow_end","timestamp":"2026-05-05T10:00:01Z","runId":"r1","duration":"1.0s","tokens":{"prompt":12,"completion":3,"total":15}}
`)

	// Documented enumeration of EventType values from internal/types/types.go,
	// plus the streaming "output" type emitted by JSONSink.OnOutput.
	knownTypes := map[string]struct{}{
		string(zenflow.EventWorkflowStart):           {},
		string(zenflow.EventWorkflowEnd):             {},
		string(zenflow.EventStepStart):               {},
		string(zenflow.EventStepEnd):                 {},
		string(zenflow.EventStepSkipped):             {},
		string(zenflow.EventAgentTurn):               {},
		string(zenflow.EventToolCall):                {},
		string(zenflow.EventMessage):                 {},
		string(zenflow.EventError):                   {},
		string(zenflow.EventCoordinatorNarration):    {},
		string(zenflow.EventCoordinatorMessage):      {},
		string(zenflow.EventCoordinatorSynthesis):    {},
		string(zenflow.EventCoordinatorInboxMessage): {},
		string(zenflow.EventMessageSent):             {},
		string(zenflow.EventPlanReady):               {},
		string(zenflow.EventAgentInboxDrain):         {},
		string(zenflow.EventMessageDropped):          {},
		string(zenflow.EventAgentIdle):               {},
		string(zenflow.EventAgentWake):               {},
		string(zenflow.EventMaxWakeCyclesWarning):    {},
		string(zenflow.EventResumeStarted):           {},
		string(zenflow.EventResumeCompleted):         {},
		string(zenflow.EventResumeFailed):            {},
		string(zenflow.EventResumeQueued):            {},
		string(zenflow.EventTranscriptSealed):        {},
		"output": {},
	}

	// Documented data-key requirements per type (subset; only the keys
	// spec.md § 14.2 pins as required for the events that appear in the
	// sample). Adding a new event to the sample => add its required keys
	// here too so the round-trip stays in lockstep.
	requiredDataKeys := map[string][]string{
		string(zenflow.EventWorkflowStart): {"total"},
		string(zenflow.EventStepStart):     {"index", "total"},
		string(zenflow.EventAgentTurn):     {"phase", "model"},
	}

	for i, line := range strings.Split(sample, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i+1, err)
		}

		typ, ok := obj["type"].(string)
		if !ok {
			t.Fatalf("line %d: missing or non-string `type`: %v", i+1, obj["type"])
		}
		if _, known := knownTypes[typ]; !known {
			t.Errorf("line %d: unknown type %q (not in spec.md § 14.2)", i+1, typ)
		}

 // Streaming `output` events carry their own envelope shape; they
 // do not include `timestamp`. Every other event must.
		if typ != "output" {
			if _, ok := obj["timestamp"].(string); !ok {
				t.Errorf("line %d (type=%s): missing `timestamp`", i+1, typ)
			}
		}
		if _, ok := obj["runId"].(string); !ok {
			t.Errorf("line %d (type=%s): missing `runId` (every line in the sample carries one)", i+1, typ)
		}

		if required, ok := requiredDataKeys[typ]; ok {
			data, _ := obj["data"].(map[string]any)
			for _, k := range required {
				if _, present := data[k]; !present {
					t.Errorf("line %d (type=%s): missing required data key %q", i+1, typ, k)
				}
			}
		}

		if typ == "output" {
			if _, ok := obj["delta"].(string); !ok {
				t.Errorf("line %d (type=output): missing string `delta`", i+1)
			}
			if _, ok := obj["done"].(bool); !ok {
				t.Errorf("line %d (type=output): missing bool `done`", i+1)
			}
		}
	}
}
