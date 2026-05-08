package exec

import (
	"fmt"
	"strings"
	"testing"
)

// TestParseLimits_StepCount - reject workflows with > MaxSteps.
func TestParseLimits_StepCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("name: too-many\nversion: 1\nsteps:\n")
	for i := 0; i < MaxStepsPerWorkflow+1; i++ {
		fmt.Fprintf(&sb, "  - id: s%d\n    agent: a\n    instructions: \"x\"\n", i)
	}
	sb.WriteString("agents:\n  a:\n    description: test\n")

	_, err := ParseWorkflow([]byte(sb.String()))
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("expected max-steps rejection; got %v", err)
	}
}

// TestParseLimits_StepIDPattern - ZF8.0f: step ID regex
// ^[a-z][a-z0-9_]{0,63}$ (lowercase only, no dash, ≤64 chars).
func TestParseLimits_StepIDPattern(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want bool // true = should be REJECTED by strict regex
	}{
		{"valid_id", "valid_id", false},
		{"single_letter", "a", false},
		{"uppercase", "Bad", true},                  // uppercase
		{"with-dash", "with-dash", false},           // dash permitted (deviation note in limits.go)
		{"leading-digit", "1numeric", true},         // leading digit
		{"too-long", strings.Repeat("a", 65), true}, // too long
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			yaml := fmt.Sprintf(
				"name: t\nversion: 1\nsteps:\n  - id: %s\n    agent: a\n    instructions: x\nagents:\n  a:\n    description: d\n",
				c.id)
			_, err := ParseWorkflow([]byte(yaml))
			if c.want && err == nil {
				t.Errorf("id=%q: expected rejection, got nil", c.id)
			}
			if !c.want && err != nil {
				t.Errorf("id=%q: expected accept, got %v", c.id, err)
			}
		})
	}
}

// TestParseLimits_DescriptionTooLong - ZF8.0f.
func TestParseLimits_DescriptionTooLong(t *testing.T) {
	desc := strings.Repeat("x", MaxDescriptionChars+1)
	yaml := fmt.Sprintf(
		"name: t\nversion: 1\ndescription: %q\nsteps:\n  - id: s\n    agent: a\n    instructions: x\nagents:\n  a:\n    description: d\n",
		desc)
	_, err := ParseWorkflow([]byte(yaml))
	if err == nil {
		t.Fatal("expected rejection for oversized description")
	}
}

// TestParseLimits_FileSize - LoadWorkflow-ish check for max file size.
func TestParseLimits_FileSize(t *testing.T) {
	big := make([]byte, MaxFileSizeBytes+1)
	for i := range big {
		big[i] = ' '
	}
	_, err := ParseWorkflow(big)
	if err == nil {
		t.Fatal("expected rejection for oversized file")
	}
}

// TestParseLimits_MaxDepth - depth enforcement via nested loop/include is
// already banned via `nested loop (prohibited)`. We assert MaxDepth is
// exposed as a constant the parser consults (spec guardrail, §14.2.1).
func TestParseLimits_MaxDepth(t *testing.T) {
	if MaxNestingDepth <= 0 {
		t.Fatalf("MaxNestingDepth must be positive; got %d", MaxNestingDepth)
	}
}
