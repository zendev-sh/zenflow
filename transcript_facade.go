package zenflow

import (
	"github.com/zendev-sh/zenflow/internal/resume"
)

// TranscriptStore re-exports resume.TranscriptStore so existing
// consumers can keep importing it from the zenflow root package.
// Stable.
type TranscriptStore = resume.TranscriptStore

// StepTranscript re-exports resume.StepTranscript.
// Stable.
type StepTranscript = resume.StepTranscript

// TranscriptTruncatedLoader re-exports resume.TranscriptTruncatedLoader.
// Stable.
type TranscriptTruncatedLoader = resume.TranscriptTruncatedLoader

// MetadataSetter re-exports resume.MetadataSetter.
// Stable.
type MetadataSetter = resume.MetadataSetter

// InMemoryTranscriptStore re-exports resume.InMemoryTranscriptStore.
// Stable.
type InMemoryTranscriptStore = resume.InMemoryTranscriptStore

// InMemoryTranscriptStoreOption re-exports
// resume.InMemoryTranscriptStoreOption.
// Stable.
type InMemoryTranscriptStoreOption = resume.InMemoryTranscriptStoreOption

// Sentinel errors re-exported from resume.
var (
	// ErrNoTranscript re-exports resume.ErrNoTranscript.
	// Stable.
	ErrNoTranscript = resume.ErrNoTranscript

	// ErrTranscriptTooLarge re-exports resume.ErrTranscriptTooLarge.
	// Stable.
	ErrTranscriptTooLarge = resume.ErrTranscriptTooLarge
)

// NewInMemoryTranscriptStore re-exports resume.NewInMemoryTranscriptStore. Stable.
var NewInMemoryTranscriptStore = resume.NewInMemoryTranscriptStore

// NewInMemoryTranscriptStoreWithOptions re-exports resume.NewInMemoryTranscriptStoreWithOptions. Stable.
var NewInMemoryTranscriptStoreWithOptions = resume.NewInMemoryTranscriptStoreWithOptions

// NewInMemoryTranscriptStoreWithCaps re-exports resume.NewInMemoryTranscriptStoreWithCaps. Stable.
var NewInMemoryTranscriptStoreWithCaps = resume.NewInMemoryTranscriptStoreWithCaps

// WithTranscriptCaps re-exports resume.WithTranscriptCaps. Stable.
var WithTranscriptCaps = resume.WithTranscriptCaps

// DefaultTruncatedResumeMessages re-exports resume.DefaultTruncatedResumeMessages
// so external consumers can read or mirror the executor's tail bound for
// LoadTruncated when no explicit cap is supplied.
// Stable.
const DefaultTruncatedResumeMessages = resume.DefaultTruncatedResumeMessages

// DefaultMaxTranscriptMessages re-exports resume.DefaultMaxTranscriptMessages
// so external consumers (and the WithMaxTranscriptMessages docstring) can
// reference the canonical default per-step message-count cap applied to
// the Day-1 InMemoryTranscriptStore.
// Stable.
const DefaultMaxTranscriptMessages = resume.DefaultMaxTranscriptMessages

// DefaultMaxTranscriptBytes re-exports resume.DefaultMaxTranscriptBytes
// so external consumers (and the WithMaxTranscriptBytes docstring) can
// reference the canonical default per-step byte cap applied to the
// Day-1 InMemoryTranscriptStore.
// Stable.
const DefaultMaxTranscriptBytes = resume.DefaultMaxTranscriptBytes
