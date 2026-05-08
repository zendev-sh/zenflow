package resume

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/zendev-sh/goai/provider"
)

func mkTextMsg(role provider.Role, text string) provider.Message {
	return provider.Message{
		Role:    role,
		Content: []provider.Part{{Type: provider.PartText, Text: text}},
	}
}

func TestResumeR1_InMemTranscript_AppendLoadDelete(t *testing.T) {
	s := NewInMemoryTranscriptStore()

	if err := s.Append("run-1", "step-a", []provider.Message{
		mkTextMsg(provider.RoleUser, "hello"),
		mkTextMsg(provider.RoleAssistant, "hi"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Load("run-1", "step-a")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got.Messages))
	}
	if got.StepID != "step-a" || got.RunID != "run-1" {
		t.Fatalf("wrong ids: %+v", got)
	}
	if got.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("first message wrong: %+v", got.Messages[0])
	}

	// Load returns a defensive copy - mutating it does not affect store.
	got.Messages[0] = mkTextMsg(provider.RoleUser, "MUTATED")
	got2, _ := s.Load("run-1", "step-a")
	if got2.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("defensive copy failed: %q", got2.Messages[0].Content[0].Text)
	}

	// Delete is idempotent.
	if err := s.Delete("run-1", "step-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete("run-1", "step-a"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
	if _, err := s.Load("run-1", "step-a"); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("post-delete Load want ErrNoTranscript, got %v", err)
	}
}

func TestResumeR1_InMemTranscript_LoadMissingReturnsErrNoTranscript(t *testing.T) {
	s := NewInMemoryTranscriptStore()
	if _, err := s.Load("run-x", "step-x"); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("want ErrNoTranscript, got %v", err)
	}
}

func TestResumeR1_InMemTranscript_AppendEmptyIsNoop(t *testing.T) {
	s := NewInMemoryTranscriptStore()
	if err := s.Append("r", "s", nil); err != nil {
		t.Fatalf("Append nil: %v", err)
	}
	if _, err := s.Load("r", "s"); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("empty Append should not create transcript, got %v", err)
	}
}

func TestResumeR1_InMemTranscript_MaxMessagesCap(t *testing.T) {
	s := NewInMemoryTranscriptStoreWithCaps(3, 0)
	msgs := []provider.Message{
		mkTextMsg(provider.RoleUser, "a"),
		mkTextMsg(provider.RoleUser, "b"),
	}
	if err := s.Append("r", "s", msgs); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	// Now at 2/3. Appending 1 more is OK.
	if err := s.Append("r", "s", []provider.Message{mkTextMsg(provider.RoleUser, "c")}); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	// Appending another would push to 4/3 - reject.
	err := s.Append("r", "s", []provider.Message{mkTextMsg(provider.RoleUser, "d")})
	if !errors.Is(err, ErrTranscriptTooLarge) {
		t.Fatalf("want ErrTranscriptTooLarge, got %v", err)
	}
	// F3: after cap-hit the slot is sealed and Load also surfaces
	// ErrTranscriptTooLarge so Router can emit
	// DropReasonTranscriptTooLarge rather than silently resume from a
	// truncated transcript.
	_, loadErr := s.Load("r", "s")
	if !errors.Is(loadErr, ErrTranscriptTooLarge) {
		t.Fatalf("sealed Load must surface ErrTranscriptTooLarge, got %v", loadErr)
	}
}

func TestResumeR1_InMemTranscript_MaxBytesCap(t *testing.T) {
	// Set bytes cap low; 32 B envelope overhead + text length.
	// Each message "a"*200 = 200+32=232 bytes.
	s := NewInMemoryTranscriptStoreWithCaps(0, 500)
	big := make([]byte, 200)
	for i := range big {
		big[i] = 'a'
	}
	msg := mkTextMsg(provider.RoleUser, string(big))

	if err := s.Append("r", "s", []provider.Message{msg}); err != nil {
		t.Fatalf("Append 1 (232B): %v", err)
	}
	if err := s.Append("r", "s", []provider.Message{msg}); err != nil {
		t.Fatalf("Append 2 (464B): %v", err)
	}
	// Third would push past 500.
	err := s.Append("r", "s", []provider.Message{msg})
	if !errors.Is(err, ErrTranscriptTooLarge) {
		t.Fatalf("want ErrTranscriptTooLarge, got %v", err)
	}
}

func TestResumeR1_InMemTranscript_ConcurrentAppend(t *testing.T) {
	s := NewInMemoryTranscriptStoreWithCaps(10000, 1<<30)
	const goroutines = 16
	const perG = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_ = s.Append("run", "step", []provider.Message{
					mkTextMsg(provider.RoleUser, fmt.Sprintf("g%d-%d", idx, i)),
				})
			}
		}(g)
	}
	wg.Wait()

	got, err := s.Load("run", "step")
	if err != nil {
		t.Fatalf("Load after concurrent: %v", err)
	}
	if len(got.Messages) != goroutines*perG {
		t.Fatalf("want %d messages, got %d", goroutines*perG, len(got.Messages))
	}
}

func TestResumeR1_InMemTranscript_Metadata(t *testing.T) {
	s := NewInMemoryTranscriptStore()
	s.SetMetadata("r", "s", "you are helpful", "goai:test-model")
	// Append some messages.
	_ = s.Append("r", "s", []provider.Message{mkTextMsg(provider.RoleUser, "x")})

	got, err := s.Load("r", "s")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SystemPrompt != "you are helpful" {
		t.Fatalf("SystemPrompt: %q", got.SystemPrompt)
	}
	if got.Model != "goai:test-model" {
		t.Fatalf("Model: %q", got.Model)
	}
}

func TestResumeR1_InMemTranscript_MultipleSteps(t *testing.T) {
	s := NewInMemoryTranscriptStore()
	_ = s.Append("r", "step-1", []provider.Message{mkTextMsg(provider.RoleUser, "1a")})
	_ = s.Append("r", "step-2", []provider.Message{mkTextMsg(provider.RoleUser, "2a")})

	t1, _ := s.Load("r", "step-1")
	t2, _ := s.Load("r", "step-2")
	if t1.Messages[0].Content[0].Text != "1a" || t2.Messages[0].Content[0].Text != "2a" {
		t.Fatalf("cross-step leak")
	}

	// Deleting one does not affect the other.
	_ = s.Delete("r", "step-1")
	if _, err := s.Load("r", "step-1"); !errors.Is(err, ErrNoTranscript) {
		t.Fatalf("step-1 should be gone")
	}
	if _, err := s.Load("r", "step-2"); err != nil {
		t.Fatalf("step-2 should still exist: %v", err)
	}
}
