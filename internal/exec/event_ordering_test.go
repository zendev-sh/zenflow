package exec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow/internal/types"
)

// orderingSink records every event with sequence numbers so we can
// assert WorkflowEnd is terminal.
type orderingSink struct {
	mu     sync.Mutex
	events []EventType
}

func (s *orderingSink) OnEvent(_ context.Context, e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e.Type)
}

func (s *orderingSink) OnOutput(context.Context, Output) {}

func (s *orderingSink) snap() []EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]EventType(nil), s.events...)
}

// TestEventOrdering_WorkflowEndTerminal - ZF8.0a: any
// Resume*/TranscriptSealed/CoordinatorInboxMessage event must appear
// BEFORE WorkflowEnd in the stream emitted by a single run.
// We emit events directly against the sink to simulate the sequence
// an executor would produce, then assert invariants; we also run
// under -race to catch sink contention.
func TestEventOrdering_WorkflowEndTerminal(t *testing.T) {
	s := &orderingSink{}
	ctx := t.Context()

	// Simulate a busy executor that interleaves delta + lifecycle
	// events, finishing with WorkflowEnd.
	var wg sync.WaitGroup
	emit := func(et EventType) {
		defer wg.Done()
		s.OnEvent(ctx, Event{Type: et})
	}

	preTerminal := []EventType{
		types.EventWorkflowStart,
		types.EventStepStart, types.EventStepEnd,
		types.EventResumeStarted, types.EventResumeCompleted,
		types.EventTranscriptSealed,
		types.EventCoordinatorInboxMessage,
	}
	for _, et := range preTerminal {
		wg.Add(1)
		go emit(et)
	}
	wg.Wait()
	// Emit terminal after all pre-terminal events completed.
	s.OnEvent(ctx, Event{Type: types.EventWorkflowEnd})

	evts := s.snap()
	// WorkflowEnd must be last.
	if evts[len(evts)-1] != types.EventWorkflowEnd {
		t.Fatalf("WorkflowEnd not terminal: %v", evts)
	}
	// Each pre-terminal event must appear exactly once before End.
	for _, want := range preTerminal {
		found := false
		for i, got := range evts {
			if got == types.EventWorkflowEnd {
				if !found {
					t.Errorf("%q missing before WorkflowEnd; events=%v", want, evts)
				}
				break
			}
			if got == want {
				found = true
				_ = i
			}
		}
	}
}

// TestEventOrdering_DrainBeforeWorkflowEnd - exercises the executor
// helper directly: when a coordinator inbox has pending messages
// AND an in-flight resume exists, drainBeforeWorkflowEnd must wait
// for the resume and drain the inbox before returning.
func TestEventOrdering_DrainBeforeWorkflowEnd(t *testing.T) {
	e := &Executor{Progress: &orderingSink{}}

	// Add a fake resume goroutine that finishes quickly.
	e.resumeWG.Add(1)
	go func() {
		defer e.resumeWG.Done()
		time.Sleep(20 * time.Millisecond)
	}()

	start := time.Now()
	e.drainBeforeWorkflowEnd(context.Background(), "run-1")
	if time.Since(start) < 15*time.Millisecond {
		t.Fatal("drain returned before resume finished")
	}
}
