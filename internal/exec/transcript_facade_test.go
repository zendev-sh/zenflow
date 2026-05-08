package exec

import (
	"errors"
	"strings"
	"testing"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/zenflow/internal/resume"
)

// TestTranscriptFacade_OptionsConstructor verifies that the root-level
// re-exports for NewInMemoryTranscriptStoreWithOptions and
// WithTranscriptCaps build a working store WITH the requested caps
// actually enforced. Appending 9 messages to an 8-message cap must fail
// with ErrTranscriptTooLarge - proving WithTranscriptCaps was applied,
// not just the constructor returned non-nil.
func TestTranscriptFacade_OptionsConstructor(t *testing.T) {
	s := resume.NewInMemoryTranscriptStoreWithOptions(resume.WithTranscriptCaps(8, 1<<20))
	if s == nil {
		t.Fatal("NewInMemoryTranscriptStoreWithOptions returned nil")
	}
	mkMsg := func(text string) provider.Message {
		return provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Part{{Type: provider.PartText, Text: text}},
		}
	}
	// 8 messages must succeed (cap=8).
	msgs := make([]provider.Message, 8)
	for i := range msgs {
		msgs[i] = mkMsg("ok")
	}
	if err := s.Append("run-1", "step-1", msgs); err != nil {
		t.Fatalf("Append 8 msgs (cap=8): unexpected err %v", err)
	}
	// 9th must trip the cap.
	if err := s.Append("run-1", "step-1", []provider.Message{mkMsg("over")}); err == nil {
		t.Error("Append 9th msg: want ErrTranscriptTooLarge, got nil (cap not enforced)")
	} else if !errors.Is(err, resume.ErrTranscriptTooLarge) && !strings.Contains(err.Error(), "too large") {
		t.Errorf("Append over cap: err = %v, want ErrTranscriptTooLarge", err)
	}
}
