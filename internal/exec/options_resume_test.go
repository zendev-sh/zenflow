package exec

import (
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/resume"
)

// TestOptions_ResumeCoverage exercises the Options:
// WithTranscriptStore, WithMaxTranscriptMessages, WithMaxTranscriptBytes,
// WithExternalInbox, WithModelResolver, WithTruncationOnCapReached.
func TestOptions_ResumeCoverage(t *testing.T) {
	factoryCalled := 0
	factory := func() resume.TranscriptStore {
		factoryCalled++
		return resume.NewInMemoryTranscriptStore()
	}
	resolverCalled := 0
	resolver := func(id string) (provider.LanguageModel, error) {
		resolverCalled++
		return nil, nil
	}

	o := New(
		WithTranscriptStore(factory),
		WithMaxTranscriptMessages(1234),
		WithMaxTranscriptBytes(9876),
		WithExternalInbox("coordinator", "watcher"),
		WithExternalInbox("extra"),
		WithModelResolver(resolver),
		WithTruncationOnCapReached(),
	)

	if o.transcriptStoreFactory == nil {
		t.Fatal("transcriptStoreFactory nil")
	}
	// Invoke factory to confirm it's the one we installed.
	if s := o.transcriptStoreFactory(); s == nil {
		t.Fatal("factory returned nil")
	}
	if factoryCalled != 1 {
		t.Fatalf("factoryCalled=%d want 1", factoryCalled)
	}
	if o.maxTranscriptMessages != 1234 {
		t.Fatalf("maxTranscriptMessages=%d want 1234", o.maxTranscriptMessages)
	}
	if o.maxTranscriptBytes != 9876 {
		t.Fatalf("maxTranscriptBytes=%d want 9876", o.maxTranscriptBytes)
	}
	if got, want := len(o.externalInboxes), 3; got != want {
		t.Fatalf("externalInboxes len=%d want %d (%v)", got, want, o.externalInboxes)
	}
	if o.externalInboxes[0] != "coordinator" || o.externalInboxes[2] != "extra" {
		t.Fatalf("externalInboxes=%v", o.externalInboxes)
	}
	if o.modelResolver == nil {
		t.Fatal("modelResolver nil")
	}
	if _, _ = o.modelResolver("x"); resolverCalled != 1 {
		t.Fatalf("resolverCalled=%d want 1", resolverCalled)
	}
	if !o.truncateOnCapReached {
		t.Fatal("truncateOnCapReached should be true")
	}

	// Thread through to Executor.
	exec := &Executor{}
	o.applyExecutorOptions(exec)
	if exec.TranscriptStoreFactory == nil {
		t.Fatal("exec.TranscriptStoreFactory not threaded")
	}
	if exec.MaxTranscriptMessages != 1234 {
		t.Fatalf("exec.MaxTranscriptMessages=%d want 1234", exec.MaxTranscriptMessages)
	}
	if exec.MaxTranscriptBytes != 9876 {
		t.Fatalf("exec.MaxTranscriptBytes=%d want 9876", exec.MaxTranscriptBytes)
	}
	if len(exec.ExternalInboxes) != 3 {
		t.Fatalf("exec.ExternalInboxes len=%d want 3", len(exec.ExternalInboxes))
	}
	if exec.ModelResolver == nil {
		t.Fatal("exec.ModelResolver not threaded")
	}
	if !exec.TruncateOnCapReached {
		t.Fatal("exec.TruncateOnCapReached not threaded")
	}
}
