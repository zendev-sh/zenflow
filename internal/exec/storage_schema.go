package exec

import (
	"cmp"
	"encoding/json"
	"errors"

	"github.com/zendev-sh/goai/provider"
)

// tokensSchema is the stable on-disk representation of provider.Usage.
// provider.Usage has no json tags, so its fields serialize as PascalCase
// (InputTokens, OutputTokens, …). If goai ever adds json tags those names
// would change and existing run.json files would decode to zero silently.
// This wrapper pins the on-disk names to lowercase snake_case and provides a
// custom UnmarshalJSON that also accepts the legacy PascalCase form so
// existing files continue to load correctly.
type tokensSchema struct {
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	Total      int `json:"total,omitempty"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
}

// UnmarshalJSON accepts both the new lowercase form and the legacy PascalCase
// form emitted by untagged provider.Usage embedding.
// New form: {"input":10,"output":5,...}
// Old form: {"InputTokens":10,"OutputTokens":5,...}
// Merge strategy: both forms are decoded independently, then each field is
// resolved with cmp.Or(newForm.X, legacy.X). This preserves data from
// mixed-format payloads written by partially-migrated writers - e.g.
// {"input":100,"OutputTokens":200} yields Input=100, Output=200 rather than
// silently dropping the PascalCase field.
func (t *tokensSchema) UnmarshalJSON(data []byte) error {
	// Decode new lowercase form (alias prevents infinite recursion).
	type alias tokensSchema
	var newForm alias
	if err := json.Unmarshal(data, &newForm); err != nil {
		return err
	}
	// Decode legacy PascalCase form (untagged provider.Usage).
	// The error is intentionally ignored: data was already validated as valid
	// JSON by the first unmarshal above. Both calls decode the same bytes into
	// plain structs with no custom unmarshalers, so the second call cannot fail
	// when the first succeeded.
	var legacy struct {
		InputTokens      int `json:"InputTokens"`
		OutputTokens     int `json:"OutputTokens"`
		TotalTokens      int `json:"TotalTokens"`
		CacheReadTokens  int `json:"CacheReadTokens"`
		CacheWriteTokens int `json:"CacheWriteTokens"`
		ReasoningTokens  int `json:"ReasoningTokens"`
	}
	_ = json.Unmarshal(data, &legacy) //nolint:errcheck // unreachable: same data already parsed above
	// Per-field merge: new form wins when non-zero, else fall back to legacy.
	t.Input = cmp.Or(newForm.Input, legacy.InputTokens)
	t.Output = cmp.Or(newForm.Output, legacy.OutputTokens)
	t.Total = cmp.Or(newForm.Total, legacy.TotalTokens)
	t.CacheRead = cmp.Or(newForm.CacheRead, legacy.CacheReadTokens)
	t.CacheWrite = cmp.Or(newForm.CacheWrite, legacy.CacheWriteTokens)
	t.Reasoning = cmp.Or(newForm.Reasoning, legacy.ReasoningTokens)
	return nil
}

func tokensToSchema(u provider.Usage) tokensSchema {
	return tokensSchema{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		Total:      u.TotalTokens,
		CacheRead:  u.CacheReadTokens,
		CacheWrite: u.CacheWriteTokens,
		Reasoning:  u.ReasoningTokens,
	}
}

func tokensFromSchema(t tokensSchema) provider.Usage {
	return provider.Usage{
		InputTokens:      t.Input,
		OutputTokens:     t.Output,
		TotalTokens:      t.Total,
		CacheReadTokens:  t.CacheRead,
		CacheWriteTokens: t.CacheWrite,
		ReasoningTokens:  t.Reasoning,
	}
}

// errorSchema is the stable on-disk representation of an error value.
// The original format stored errors as a plain JSON string, which flattened
// multi-error structure (errors.Join) to a single text blob. The new format
// stores an array of messages and a "joined" flag so that errors.Is checks
// survive a round-trip for wrapped/joined errors.
// Custom UnmarshalJSON accepts both:
// - Legacy string form: "some error text"
// - New object form: {"messages":["e1","e2"],"joined":true}
type errorSchema struct {
	Messages []string `json:"messages,omitempty"`
	Joined   bool     `json:"joined,omitempty"`
}

// UnmarshalJSON accepts both legacy string form and new object form.
func (e *errorSchema) UnmarshalJSON(data []byte) error {
	// Detect the payload shape by its first non-whitespace byte.
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
 // Legacy form: plain JSON string.
			var msg string
			if err := json.Unmarshal(data, &msg); err != nil {
				return err
			}
			if msg != "" {
				e.Messages = []string{msg}
			}
			e.Joined = false
			return nil
		default:
 // New form: object.
			type alias errorSchema
			var obj alias
			if err := json.Unmarshal(data, &obj); err != nil {
				return err
			}
			*e = errorSchema(obj)
			return nil
		}
	}
	return nil
}

func errorToSchema(err error) errorSchema {
	if err == nil {
		return errorSchema{}
	}
	// Check for errors.Join / multi-error (Unwrap []error interface).
	type unwrapMulti interface {
		Unwrap() []error
	}
	var joined unwrapMulti
	if errors.As(err, &joined) {
		errs := joined.Unwrap()
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		return errorSchema{Messages: msgs, Joined: true}
	}
	return errorSchema{Messages: []string{err.Error()}}
}

func errorFromSchema(s errorSchema) error {
	if len(s.Messages) == 0 {
		return nil
	}
	if !s.Joined || len(s.Messages) == 1 {
		return errors.New(s.Messages[0])
	}
	errs := make([]error, len(s.Messages))
	for i, m := range s.Messages {
		errs[i] = errors.New(m)
	}
	return errors.Join(errs...)
}
