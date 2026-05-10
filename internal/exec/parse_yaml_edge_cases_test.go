package exec

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestEdge_TabIndentation verifies that tab characters used for YAML
// indentation are rejected with a typed *ValidationError.
// (2026-05-04) - was a silent-pass test that logged the type
// without enforcing it. Now uses errors.As + typed-error contract.
func TestEdge_TabIndentation(t *testing.T) {
	tabYAML := "name: tab-test\nsteps:\n\t- id: step1\n\t  instructions: \"do\"\n"
	_, err := ParseWorkflow([]byte(tabYAML))
	if err == nil {
		t.Fatal("expected error for tab indentation, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("err = %T (%v), want *ValidationError wrap", err, err)
	}
}

// TestEdge_YAMLMergeKeys tests YAML merge key (<<) with anchors.
// gopkg.in/yaml.v3 resolves merge keys before populating structs.
// - both branches now ASSERT. Pre-fix the rejection path
// silently logged + returned, masking regressions in either direction.
func TestEdge_YAMLMergeKeys(t *testing.T) {
	anchorYAML := `
name: anchor-test
agents:
  coder: &baseAgent
    description: "base agent"
    model: gpt-4o
  reviewer:
    <<: *baseAgent
    description: "reviewer agent"
steps:
  - id: step1
    agent: reviewer
    instructions: "do"
`
	wf, err := ParseWorkflow([]byte(anchorYAML))
	if err != nil {
		// Acceptable behavior IF rejection becomes the contract: but
		// we explicitly enforce that the current parser DOES accept
		// merge keys. If this changes, the test must change too.
		t.Fatalf("yaml merge keys rejected - current parser is documented to resolve them via yaml.v3. Update spec/v1/spec.md alongside this rejection. err=%v", err)
	}
	rev, ok := wf.Agents["reviewer"]
	if !ok {
		t.Fatal("reviewer agent not found after anchor merge")
	}
	if rev.Description != "reviewer agent" {
		t.Errorf("reviewer.Description = %q, want %q", rev.Description, "reviewer agent")
	}
	if rev.Model != "gpt-4o" {
		t.Errorf("reviewer.Model = %q, want %q (merged from anchor)", rev.Model, "gpt-4o")
	}
}

// TestEdge_SimpleAlias tests *alias referencing an entire step node.
// This would produce duplicate step IDs if accepted by ValidateWorkflow.
func TestEdge_SimpleAlias(t *testing.T) {
	simpleAnchorYAML := `
name: alias-test
steps:
  - &step1
    id: step1
    instructions: "do something"
  - *step1
`
	_, err := ParseWorkflow([]byte(simpleAnchorYAML))
	if err == nil {
		t.Fatal("expected error: duplicate step ID 'step1' from alias, got nil")
	}
	// - assert the typed-error class. Either *ValidationError
	// (yaml-level rejection) or *DuplicateStepError (validate-level
	// rejection) is acceptable; both are typed.
	var ve *ValidationError
	var de *DuplicateStepError
	if !errors.As(err, &ve) && !errors.As(err, &de) {
		t.Errorf("err = %T (%v), want *ValidationError or *DuplicateStepError", err, err)
	}
}

// TestEdge_MultiDocumentYAML tests the --- separator.
// ParseWorkflow uses a single Decode call, so only the first document is parsed.
func TestEdge_MultiDocumentYAML(t *testing.T) {
	multiDocYAML := `name: multi-doc
steps:
  - id: step1
    instructions: "do"
---
`
	wf, err := ParseWorkflow([]byte(multiDocYAML))
	if err != nil {
		t.Fatalf("unexpected error for multi-doc YAML (single valid doc + empty): %v", err)
	}
	if wf.Name != "multi-doc" {
		t.Errorf("name = %q, want %q", wf.Name, "multi-doc")
	}

	// - second-decode behavior asserted. yaml.v3 returns nil
	// for an empty trailing document and leaves wf3 zero-valued.
	var wf2 Workflow
	dec := yaml.NewDecoder(bytes.NewReader([]byte(multiDocYAML)))
	dec.KnownFields(true)
	if err := dec.Decode(&wf2); err != nil {
		t.Fatalf("first Decode: unexpected error: %v", err)
	}
	var wf3 Workflow
	if err := dec.Decode(&wf3); err != nil {
		t.Errorf("second Decode on empty doc: err=%v, want nil (yaml.v3 returns nil for empty trailing docs)", err)
	}
	if wf3.Name != "" {
		t.Errorf("second Decode populated wf3.Name=%q, want empty", wf3.Name)
	}
}

// TestEdge_EmptyFile verifies that empty / whitespace-only files
// return *ValidationError (the typed wrap parse.go uses for yaml.v3 EOF).
func TestEdge_EmptyFile(t *testing.T) {
	for _, in := range []string{"", "   \n   \n"} {
		_, err := ParseWorkflow([]byte(in))
		if err == nil {
			t.Fatalf("expected error for input %q, got nil", in)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("input=%q err = %T (%v), want *ValidationError", in, err, err)
		}
	}
}

// TestEdge_UTFBOM verifies BOM handling. yaml.v3 strips the BOM
// internally before parsing the stream, so wf.Name should be free of
// BOM bytes.
// - both branches now assert. Pre-fix the rejection path
// silently returned without any typed-error check.
func TestEdge_UTFBOM(t *testing.T) {
	bomYAML := "\xef\xbb\xbfname: bom-test\nsteps:\n  - id: step1\n    instructions: \"do\"\n"
	wf, err := ParseWorkflow([]byte(bomYAML))
	if err != nil {
		// Rejection is acceptable IF the parser's contract is
		// documented as such. Force the contract to be explicit:
		// either accept and strip, or reject with a typed error.
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("BOM rejection used non-typed error: %T (%v)", err, err)
		}
		return
	}
	if strings.HasPrefix(wf.Name, "\xef\xbb\xbf") {
		t.Errorf("BOM leaked into wf.Name: %q - yaml.v3 accepted BOM but did not strip it", wf.Name)
	}
	if wf.Name != "bom-test" {
		t.Errorf("wf.Name = %q, want %q", wf.Name, "bom-test")
	}
}

// TestEdge_CRLFLineEndings verifies that Windows CRLF line endings do not
// leak \r into string field values.
func TestEdge_CRLFLineEndings(t *testing.T) {
	crlfYAML := "name: crlf-test\r\nsteps:\r\n  - id: step1\r\n    instructions: \"hello world\"\r\n"
	wf, err := ParseWorkflow([]byte(crlfYAML))
	if err != nil {
		t.Fatalf("unexpected error for CRLF input: %v", err)
	}
	if wf.Name != "crlf-test" {
		t.Errorf("name = %q, want %q", wf.Name, "crlf-test")
	}
	if strings.Contains(wf.Steps[0].Instructions, "\r") {
		t.Errorf("CRLF leaked into instructions: %q", wf.Steps[0].Instructions)
	}
	if wf.Steps[0].Instructions != "hello world" {
		t.Errorf("instructions = %q, want %q", wf.Steps[0].Instructions, "hello world")
	}
}

// TestEdge_CommentInStringValue verifies comment handling:
// - quoted strings: # is literal
// - unquoted strings: # begins a comment (YAML spec)
func TestEdge_CommentInStringValue(t *testing.T) {
	quotedYAML := "name: comment-q\nsteps:\n  - id: step1\n    instructions: \"do # not skip\"\n"
	wf, err := ParseWorkflow([]byte(quotedYAML))
	if err != nil {
		t.Fatalf("quoted comment: unexpected error: %v", err)
	}
	if wf.Steps[0].Instructions != "do # not skip" {
		t.Errorf("quoted: instructions = %q, want %q", wf.Steps[0].Instructions, "do # not skip")
	}

	unquotedYAML := "name: comment-uq\nsteps:\n  - id: step1\n    instructions: do # not skip\n"
	wf2, err2 := ParseWorkflow([]byte(unquotedYAML))
	if err2 != nil {
		t.Fatalf("unquoted comment: unexpected error: %v", err2)
	}
	instr := wf2.Steps[0].Instructions
	if instr != "do" {
		t.Errorf("unquoted comment: instructions = %q, want %q", instr, "do")
	}
	if strings.Contains(instr, "not skip") {
		t.Errorf("unquoted comment: 'not skip' leaked into value: %q", instr)
	}
}

// TestEdge_UnknownFieldRejection verifies KnownFields(true) rejects
// unknown fields in steps, top level, and agent config - and that the
// error is a typed *ValidationError.
func TestEdge_UnknownFieldRejection(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "step",
			yaml: `name: x
steps:
  - id: s
    instructions: do
    unknownProp: nope
`,
		},
		{
			name: "top-level",
			yaml: `name: x
unknownTopField: nope
steps:
  - id: s
    instructions: do
`,
		},
		{
			name: "agent",
			yaml: `name: x
agents:
  coder:
    description: codes
    unknownAgentProp: nope
steps:
  - id: s
    agent: coder
    instructions: do
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("unknown %s field NOT rejected - KnownFields(true) not active", tc.name)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("err = %T (%v), want *ValidationError", err, err)
			}
		})
	}
}

// TestEdge_DurationCoercion verifies what Duration types the parser
// accepts. Integer values must be rejected; bare scalar and quoted
// string forms must succeed and produce equal Duration values.
func TestEdge_DurationCoercion(t *testing.T) {
	intDurYAML := "name: dur-int\nsteps:\n  - id: step1\n    instructions: \"do\"\n    timeout: 30\n"
	if _, err := ParseWorkflow([]byte(intDurYAML)); err == nil {
		t.Errorf("timeout: 30 (integer) was accepted - Duration.UnmarshalYAML expects string")
	}

	bareDurYAML := "name: dur-bare\nsteps:\n  - id: step1\n    instructions: \"do\"\n    timeout: 30s\n"
	wf, err := ParseWorkflow([]byte(bareDurYAML))
	if err != nil {
		t.Errorf("timeout: 30s (bare scalar) unexpected error: %v", err)
	} else if wf.Steps[0].Timeout.D() != 30*time.Second {
		t.Errorf("bare scalar duration = %v, want 30s", wf.Steps[0].Timeout.D())
	}

	quotedDurYAML := "name: dur-quoted\nsteps:\n  - id: step1\n    instructions: \"do\"\n    timeout: \"30s\"\n"
	wf2, err2 := ParseWorkflow([]byte(quotedDurYAML))
	if err2 != nil {
		t.Errorf("timeout: \"30s\" (quoted) unexpected error: %v", err2)
	} else if wf2.Steps[0].Timeout.D() != 30*time.Second {
		t.Errorf("quoted duration = %v, want 30s", wf2.Steps[0].Timeout.D())
	}
}

// TestEdge_Version0AppliesDefault pins the contract: when YAML omits
// `version:` (Go zero), ApplyDefaults promotes Version to the
// schema-declared default (1). Setting `version: 0` explicitly is
// indistinguishable from omitting and is treated the same way.
func TestEdge_Version0AppliesDefault(t *testing.T) {
	ver0YAML := "name: ver0\nversion: 0\nsteps:\n  - id: step1\n    instructions: \"do\"\n"
	wf, err := ParseWorkflow([]byte(ver0YAML))
	if err != nil {
		t.Fatalf("version: 0 unexpectedly rejected: %v", err)
	}
	if wf.Version != 1 {
		t.Errorf("wf.Version = %d, want 1 (ApplyDefaults sets the schema default when zero)", wf.Version)
	}
}

// TestEdge_MaxConcurrencySchemaVsParseGap pins the same int-vs-*int
// gap for `options.maxConcurrency`. - was zero-assertion; now
// asserts the documented contract (parse.go accepts 0 as default).
func TestEdge_MaxConcurrencySchemaVsParseGap(t *testing.T) {
	conc0YAML := `name: conc0
steps:
  - id: step1
    instructions: "do"
options:
  maxConcurrency: 0
`
	wf, err := ParseWorkflow([]byte(conc0YAML))
	if err != nil {
		t.Fatalf("maxConcurrency: 0 unexpectedly rejected: %v", err)
	}
	if wf.Options.MaxConcurrency != 0 {
		t.Errorf("wf.Options.MaxConcurrency = %d, want 0 (Go zero / use-default semantics)", wf.Options.MaxConcurrency)
	}
}

// TestEdge_BOMNameBytes verifies BOM bytes do NOT leak into the parsed
// name when the BOM-prefixed input is accepted.
func TestEdge_BOMNameBytes(t *testing.T) {
	bomYAML := "\xef\xbb\xbfname: bom-test\nsteps:\n  - id: step1\n    instructions: \"do\"\n"
	wf, err := ParseWorkflow([]byte(bomYAML))
	if err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("BOM rejection used non-typed error: %T (%v)", err, err)
		}
		return
	}
	for i, b := range []byte(wf.Name) {
		if b == 0xEF || b == 0xBB || b == 0xBF {
			t.Errorf("BOM byte 0x%02X found at position %d in wf.Name=%q", b, i, wf.Name)
		}
	}
}

// TestEdge_MaxTurnsZeroSchemaGap pins the int-vs-*int gap for
// `agents.<name>.maxTurns`. - was zero-assertion; now asserts
// the documented contract.
func TestEdge_MaxTurnsZeroSchemaGap(t *testing.T) {
	maxTurns0YAML := `name: maxturns0
agents:
  a:
    description: "test"
    maxTurns: 0
steps:
  - id: step1
    agent: a
    instructions: "do"
`
	wf, err := ParseWorkflow([]byte(maxTurns0YAML))
	if err != nil {
		t.Fatalf("maxTurns: 0 unexpectedly rejected: %v", err)
	}
	if wf.Agents["a"].MaxTurns != 0 {
		t.Errorf("agent maxTurns = %d, want 0 (Go zero / use-default)", wf.Agents["a"].MaxTurns)
	}
}
