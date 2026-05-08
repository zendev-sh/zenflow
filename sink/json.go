package sink

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/zendev-sh/zenflow"
)

// Compile-time assertion catching signature drift on zenflow.ProgressSink
// at the type definition.
var _ zenflow.ProgressSink = (*JSONSink)(nil)

// JSONSink writes workflow events as NDJSON (one JSON object per line).
// It is safe for concurrent use.
// Construct via JSON(w). The concrete type is exported so callers can
// inspect Err after the run completes to detect I/O failures the
// streaming write path silently latched.
type JSONSink struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error // first write error encountered
}

// JSON returns a ProgressSink that writes NDJSON to the given writer.
// It is the canonical machine-readable sink for `zenflow flow --json`
// and any library consumer that wants structured event output.
func JSON(w io.Writer) *JSONSink {
	return &JSONSink{enc: json.NewEncoder(w)}
}

// OnEvent handles a workflow event by encoding it as a JSON line.
func (s *JSONSink) OnEvent(_ context.Context, event zenflow.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj := map[string]any{
		"type":      event.Type,
		"timestamp": event.Timestamp,
	}
	if event.RunID != "" {
		obj["runId"] = event.RunID
	}
	if event.StepID != "" {
		obj["stepId"] = event.StepID
	}
	if event.AgentName != "" {
		obj["agent"] = event.AgentName
	}
	if event.Message != "" {
		obj["message"] = event.Message
	}
	if event.Duration > 0 {
		obj["duration"] = event.Duration.String()
	}
	if event.Tokens != nil {
		obj["tokens"] = event.Tokens
	}
	if event.Error != nil {
		obj["error"] = event.Error.Error()
	}
	if event.Data != nil {
		obj["data"] = event.Data
	}
	if err := s.enc.Encode(obj); err != nil && s.err == nil {
		s.err = err
	}
}

// OnOutput handles streaming agent output by encoding it as a JSON line.
func (s *JSONSink) OnOutput(_ context.Context, output zenflow.Output) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj := map[string]any{
		"type":   "output",
		"runId":  output.RunID,
		"stepId": output.StepID,
		"delta":  output.Delta,
		"done":   output.Done,
	}
	if output.AgentID != "" {
		obj["agentId"] = output.AgentID
	}
	// Surface reasoning channel so JSON consumers can distinguish
	// thinking deltas from agent-text deltas. The stdout sink uses
	// different formatting for the two; JSON consumers need the flag
	// to apply equivalent rendering / filtering downstream.
	// Omit when false (zero value) to keep payload compact.
	if output.Reasoning {
		obj["reasoning"] = true
	}
	if err := s.enc.Encode(obj); err != nil && s.err == nil {
		s.err = err
	}
}

// Err returns the first write error encountered, if any.
// Consumers should check after workflow completion to detect I/O failures.
func (s *JSONSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
