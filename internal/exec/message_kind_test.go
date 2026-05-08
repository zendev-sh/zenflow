package exec

import (
	"testing"

	"github.com/zendev-sh/zenflow/internal/types"
)

// TestMessageKindConstants - ZF8.0d: canonical string values must be
// stable across the public API.
func TestMessageKindConstants(t *testing.T) {
	if types.MessageKindNotification != "notification" {
		t.Errorf("MessageKindNotification = %q; want notification", types.MessageKindNotification)
	}
	if types.MessageKindContent != "content" {
		t.Errorf("MessageKindContent = %q; want content", types.MessageKindContent)
	}
}

// TestMessageKind_TypedString verifies that MessageKind is a typed string,
// that its constants compare correctly as typed values, and that the
// Event.MessageKind field carries the new type.
func TestMessageKind_TypedString(t *testing.T) {
	// Type check: ensure constants are of type MessageKind (not plain
	// string). The explicit `MessageKind` annotation IS the assertion -
	// staticcheck's QF1011 would have us drop it, but doing so removes
	// the compile-time guarantee that MessageKind* constants stay typed.
	var _ MessageKind = types.MessageKindNotification //nolint:staticcheck // QF1011: intentional compile-time type assertion
	var _ MessageKind = types.MessageKindContent      //nolint:staticcheck // QF1011: intentional compile-time type assertion

	// Value comparison: typed constants must equal their string values.
	if string(types.MessageKindNotification) != "notification" {
		t.Errorf("string(MessageKindNotification) = %q; want notification", string(types.MessageKindNotification))
	}
	if string(types.MessageKindContent) != "content" {
		t.Errorf("string(MessageKindContent) = %q; want content", string(types.MessageKindContent))
	}

	// The two constants must be distinct.
	if types.MessageKindNotification == types.MessageKindContent {
		t.Error("MessageKindNotification and MessageKindContent must not be equal")
	}

	// Event.MessageKind field carries the typed value.
	ev := Event{MessageKind: types.MessageKindNotification}
	if ev.MessageKind != types.MessageKindNotification {
		t.Errorf("Event.MessageKind = %v; want MessageKindNotification", ev.MessageKind)
	}
	ev2 := Event{MessageKind: types.MessageKindContent}
	if ev2.MessageKind != types.MessageKindContent {
		t.Errorf("Event.MessageKind = %v; want MessageKindContent", ev2.MessageKind)
	}
}

// TestEventCarriesMessageKind - an Event{MessageKind:"content"} and
// {Subject:"x"} must round-trip through the struct (i.e. fields exist
// on the public surface).
func TestEventCarriesMessageKind(t *testing.T) {
	ev := Event{
		Type:        types.EventCoordinatorMessage,
		MessageKind: types.MessageKindContent,
		Subject:     "resume-reply",
	}
	if ev.MessageKind != types.MessageKindContent {
		t.Errorf("MessageKind missing: %+v", ev)
	}
	if ev.Subject != "resume-reply" {
		t.Errorf("Subject missing: %+v", ev)
	}
}
