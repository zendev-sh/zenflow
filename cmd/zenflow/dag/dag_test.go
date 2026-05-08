package dag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zendev-sh/zenflow"
)

// examplesGlob is the test-data path relative to this package directory.
// The canonical examples live alongside the spec in internal/exec.
const examplesGlob = "../../../internal/exec/spec/v1/examples/*.yaml"

func TestRenderDAG_AllExamples(t *testing.T) {
	examples, err := filepath.Glob(examplesGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) == 0 {
		t.Fatal("no example YAML files found")
	}

	for _, path := range examples {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			wf, err := zenflow.LoadWorkflow(path)
			if err != nil {
				t.Skipf("skip (load error): %v", err)
				return
			}
			result := Render(wf)
			if result == "" {
				t.Error("Render returned empty string")
				return
			}
 // Print for visual inspection.
			fmt.Fprintf(os.Stderr, "\n=== %s ===\n%s\n", name, result)
		})
	}
}

func TestRenderDAG_Nil(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Errorf("expected empty for nil workflow, got %q", got)
	}
}

func TestRenderDAG_Empty(t *testing.T) {
	wf := &zenflow.Workflow{Name: "empty", Steps: nil}
	if got := Render(wf); got != "" {
		t.Errorf("expected empty for workflow with no steps, got %q", got)
	}
}

func TestRenderDAG_SingleStep(t *testing.T) {
	wf := &zenflow.Workflow{
		Name:  "single",
		Steps: []zenflow.Step{{ID: "hello"}},
	}
	result := Render(wf)
	if result == "" {
		t.Fatal("empty result")
	}
	// Should contain step name and box chars.
	assertContains(t, result, "hello")
	assertContains(t, result, "┌")
	assertContains(t, result, "┘")
	// No arrows for single step.
	assertNotContains(t, result, "v")
}

func TestRenderDAG_LinearChain(t *testing.T) {
	wf := &zenflow.Workflow{
		Name: "chain",
		Steps: []zenflow.Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
			{ID: "c", DependsOn: []string{"b"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "chain (3 steps)")
	assertContains(t, result, "a")
	assertContains(t, result, "b")
	assertContains(t, result, "c")

	// Linear chain a → b → c MUST render in topological order (top-to-
	// bottom). Use unique box-content tokens (`│ a ` etc - boxes are
	// padded `│ <id><spaces>│`) to avoid false hits on the title
	// `(3 steps)` or stray border characters.
	idxA := strings.Index(result, "│ a ")
	idxB := strings.Index(result, "│ b ")
	idxC := strings.Index(result, "│ c ")
	if idxA < 0 || idxB < 0 || idxC < 0 {
		t.Fatalf("expected boxes for a/b/c; idxA=%d idxB=%d idxC=%d\n%s", idxA, idxB, idxC, result)
	}
	if idxA >= idxB || idxB >= idxC {
		t.Errorf("expected order a < b < c in rendered DAG; got idxA=%d idxB=%d idxC=%d\n%s",
			idxA, idxB, idxC, result)
	}
}

func TestRenderDAG_FanOut(t *testing.T) {
	wf := &zenflow.Workflow{
		Name: "fan",
		Steps: []zenflow.Step{
			{ID: "root"},
			{ID: "left", DependsOn: []string{"root"}},
			{ID: "mid", DependsOn: []string{"root"}},
			{ID: "right", DependsOn: []string{"root"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "fan (4 steps)")
	assertContains(t, result, "root")
	assertContains(t, result, "left")
	assertContains(t, result, "mid")
	assertContains(t, result, "right")
}

func TestRenderDAG_Diamond(t *testing.T) {
	wf := &zenflow.Workflow{
		Name: "diamond",
		Steps: []zenflow.Step{
			{ID: "start"},
			{ID: "left", DependsOn: []string{"start"}},
			{ID: "right", DependsOn: []string{"start"}},
			{ID: "end", DependsOn: []string{"left", "right"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "diamond (4 steps)")
	assertContains(t, result, "start")
	assertContains(t, result, "end")
}

func TestRenderDAG_Annotations(t *testing.T) {
	cond := "true"
	wf := &zenflow.Workflow{
		Name: "annotated",
		Steps: []zenflow.Step{
			{
				ID:           "s1",
				Agent:        "myagent",
				Loop:         &zenflow.Loop{},
				Condition:    &cond,
				Include:      "sub.yaml",
				ContextFiles: []string{"a.md", "b.md"},
			},
		},
	}
	result := Render(wf)
	assertContains(t, result, "(myagent)")
	assertContains(t, result, "[loop]")
	assertContains(t, result, "[if]")
	assertContains(t, result, "[ref]")
	assertContains(t, result, "[2 files]")

	// Test singular file.
	wf2 := &zenflow.Workflow{
		Name:  "single-file",
		Steps: []zenflow.Step{{ID: "s1", ContextFiles: []string{"a.md"}}},
	}
	result2 := Render(wf2)
	assertContains(t, result2, "[1 file]")
	assertNotContains(t, result2, "[1 files]")
}

// --- Render edge case tests ---

func TestRenderDAG_WideFanIn(t *testing.T) {
	// Multiple parents converging on one child → exercises drawConnectors merge paths.
	wf := &zenflow.Workflow{
		Name: "wide-fan-in",
		Steps: []zenflow.Step{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
			{ID: "d"},
			{ID: "merge", DependsOn: []string{"a", "b", "c", "d"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "wide-fan-in (5 steps)")
	assertContains(t, result, "merge")
}

func TestRenderDAG_DeepChain(t *testing.T) {
	// Long chain → exercises multiple vertical connector layers.
	wf := &zenflow.Workflow{
		Name: "deep",
		Steps: []zenflow.Step{
			{ID: "s1"},
			{ID: "s2", DependsOn: []string{"s1"}},
			{ID: "s3", DependsOn: []string{"s2"}},
			{ID: "s4", DependsOn: []string{"s3"}},
			{ID: "s5", DependsOn: []string{"s4"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "deep (5 steps)")
	assertContains(t, result, "s5")
}

func TestRenderDAG_ComplexDAG(t *testing.T) {
	// Mix of fan-out, fan-in, parallel branches → exercises drawConnectors edge paths.
	wf := &zenflow.Workflow{
		Name: "complex",
		Steps: []zenflow.Step{
			{ID: "root"},
			{ID: "a", DependsOn: []string{"root"}},
			{ID: "b", DependsOn: []string{"root"}},
			{ID: "c", DependsOn: []string{"root"}},
			{ID: "x", DependsOn: []string{"a", "b"}},
			{ID: "y", DependsOn: []string{"b", "c"}},
			{ID: "final", DependsOn: []string{"x", "y"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "complex (7 steps)")
	assertContains(t, result, "final")
	assertContains(t, result, "root")
}

func TestRenderDAG_ParallelRoots(t *testing.T) {
	// Multiple independent roots (no deps) → exercises first layer positioning.
	wf := &zenflow.Workflow{
		Name: "parallel-roots",
		Steps: []zenflow.Step{
			{ID: "r1"},
			{ID: "r2"},
			{ID: "r3"},
			{ID: "end", DependsOn: []string{"r1", "r2", "r3"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "parallel-roots (4 steps)")
}

// --- canvas internal tests ---

func TestCanvas_Ensure_GrowRows(t *testing.T) {
	var c canvas
	c.init(5, 2)
	// Access beyond height should grow.
	c.ensure(0, 10)
	if c.h <= 10 {
		t.Errorf("canvas height = %d, expected > 10", c.h)
	}
}

func TestCanvas_Ensure_GrowCols(t *testing.T) {
	var c canvas
	c.init(5, 5)
	// Access beyond width should grow.
	c.ensure(100, 0)
	if c.w <= 100 {
		t.Errorf("canvas width = %d, expected > 100", c.w)
	}
	// All rows should have expanded width.
	for i, row := range c.cells {
		if len(row) < c.w {
			t.Errorf("row %d width = %d, expected >= %d", i, len(row), c.w)
		}
	}
}

func TestCanvas_Ensure_GrowBoth(t *testing.T) {
	var c canvas
	c.init(3, 3)
	c.ensure(50, 50)
	if c.h <= 50 || c.w <= 50 {
		t.Errorf("canvas = %dx%d, expected > 50x50", c.w, c.h)
	}
}

func TestCanvas_GetCell_OutOfBounds(t *testing.T) {
	var c canvas
	c.init(3, 3)
	// Out of bounds should return space.
	if r := c.getCell(100, 0); r != ' ' {
		t.Errorf("getCell(100,0) = %q, want ' '", r)
	}
	if r := c.getCell(0, 100); r != ' ' {
		t.Errorf("getCell(0,100) = %q, want ' '", r)
	}
	if r := c.getCell(100, 100); r != ' ' {
		t.Errorf("getCell(100,100) = %q, want ' '", r)
	}
}

func TestCanvas_WriteStr_ExpandsCanvas(t *testing.T) {
	var c canvas
	c.init(5, 5)
	c.writeStr(0, 10, "hello world at row 10")
	if c.h <= 10 {
		t.Errorf("canvas height should have expanded, got %d", c.h)
	}
}

func TestSpaces_ZeroAndNegative(t *testing.T) {
	if s := spaces(0); s != "" {
		t.Errorf("spaces(0) = %q, want empty", s)
	}
	if s := spaces(-5); s != "" {
		t.Errorf("spaces(-5) = %q, want empty", s)
	}
}

func TestSpaces_Positive(t *testing.T) {
	if s := spaces(3); s != "   " {
		t.Errorf("spaces(3) = %q, want 3 spaces", s)
	}
}

func TestRenderDAG_EmptyLayerGroup(t *testing.T) {
	// Workflow where layer assignment could leave gaps - exercises empty group skip.
	wf := &zenflow.Workflow{
		Name: "gap",
		Steps: []zenflow.Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "a")
	assertContains(t, result, "b")
}

func TestRenderDAG_NegativeStartX(t *testing.T) {
	// Single wide step should not get negative startX - exercises startX < 0 guard.
	// Create a workflow where one layer is much wider than others.
	wf := &zenflow.Workflow{
		Name: "wide",
		Steps: []zenflow.Step{
			{ID: "tiny"},
			{ID: "very-long-step-name-that-makes-the-box-wider-than-parent", DependsOn: []string{"tiny"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "tiny")
	assertContains(t, result, "very-long")
}

func TestRenderDAG_CrossLayerPassThrough(t *testing.T) {
	// Step at layer 0 → step at layer 2 (skip layer 1) exercises pass-through.
	wf := &zenflow.Workflow{
		Name: "cross",
		Steps: []zenflow.Step{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
			{ID: "c", DependsOn: []string{"a", "b"}}, // a→c skips layer 1
		},
	}
	result := Render(wf)
	assertContains(t, result, "cross (3 steps)")
	assertContains(t, result, "a")
	assertContains(t, result, "c")
}

func TestRenderDAG_NoEdgesBetweenLayers(t *testing.T) {
	// All steps independent - no connectors needed, exercises activeSources==0 path.
	wf := &zenflow.Workflow{
		Name: "independent",
		Steps: []zenflow.Step{
			{ID: "a"},
			{ID: "b"},
			{ID: "c"},
		},
	}
	result := Render(wf)
	assertContains(t, result, "independent (3 steps)")
}

func TestRenderDAG_PassThroughRightOfSource(t *testing.T) {
	// Edge where pass-through x > origX - exercises the └──┐ branch.
	// This happens when the source box is to the right of the pass-through column.
	// Create: a wide layer 1 with source on the right, narrow layer 2.
	wf := &zenflow.Workflow{
		Name: "pt-right",
		Steps: []zenflow.Step{
			{ID: "left"},
			{ID: "right-source-with-long-name"},
			{ID: "middle", DependsOn: []string{"left"}},
			{ID: "end", DependsOn: []string{"right-source-with-long-name", "middle"}},
		},
	}
	result := Render(wf)
	assertContains(t, result, "right-source-with-long-name")
	assertContains(t, result, "end")
}

func TestDrawConnectors_NoActiveSources(t *testing.T) {
	// Direct call with no deps between groups - exercises activeSources==0 return.
	var c canvas
	c.init(50, 20)
	stepMap := map[string]zenflow.Step{
		"a": {ID: "a"},
		"b": {ID: "b"},
	}
	boxCenters := map[string]int{"a": 5, "b": 25}
	newY := drawConnectors(&c, 5, []string{"a"}, []string{"b"}, boxCenters, stepMap)
	// b doesn't depend on a → no connectors drawn, y unchanged.
	if newY != 5 {
		t.Errorf("expected y=5 (unchanged), got %d", newY)
	}
}

func TestDrawConnectors_FanOutSingleSourceMultiTarget(t *testing.T) {
	// Single source, multiple targets at different positions.
	// Exercises: atLeft && down (┌), atRight && down (┐), interior down (┬).
	var c canvas
	c.init(80, 30)
	stepMap := map[string]zenflow.Step{
		"src": {ID: "src"},
		"t1":  {ID: "t1", DependsOn: []string{"src"}},
		"t2":  {ID: "t2", DependsOn: []string{"src"}},
		"t3":  {ID: "t3", DependsOn: []string{"src"}},
	}
	boxCenters := map[string]int{"src": 20, "t1": 10, "t2": 20, "t3": 30}
	newY := drawConnectors(&c, 5, []string{"src"}, []string{"t1", "t2", "t3"}, boxCenters, stepMap)
	if newY <= 5 {
		t.Errorf("expected y > 5, got %d", newY)
	}
}

func TestDrawConnectors_MultipleSourcesToSingleTarget(t *testing.T) {
	// Multiple sources converging to single target.
	// Exercises: atLeft && up (└), atRight && up (┘), interior up (┴).
	var c canvas
	c.init(80, 30)
	stepMap := map[string]zenflow.Step{
		"s1":  {ID: "s1"},
		"s2":  {ID: "s2"},
		"s3":  {ID: "s3"},
		"tgt": {ID: "tgt", DependsOn: []string{"s1", "s2", "s3"}},
	}
	boxCenters := map[string]int{"s1": 10, "s2": 20, "s3": 30, "tgt": 20}
	newY := drawConnectors(&c, 5, []string{"s1", "s2", "s3"}, []string{"tgt"}, boxCenters, stepMap)
	if newY <= 5 {
		t.Errorf("expected y > 5, got %d", newY)
	}
}

func TestDrawConnectors_CrossPattern(t *testing.T) {
	// Sources and targets overlap at same positions.
	// Exercises: atLeft && up && down (├), atRight && up && down (┤), interior up && down (┼).
	var c canvas
	c.init(80, 30)
	stepMap := map[string]zenflow.Step{
		"s1": {ID: "s1"},
		"s2": {ID: "s2"},
		"t1": {ID: "t1", DependsOn: []string{"s1", "s2"}},
		"t2": {ID: "t2", DependsOn: []string{"s1", "s2"}},
	}
	boxCenters := map[string]int{"s1": 10, "s2": 30, "t1": 10, "t2": 30}
	newY := drawConnectors(&c, 5, []string{"s1", "s2"}, []string{"t1", "t2"}, boxCenters, stepMap)
	if newY <= 5 {
		t.Errorf("expected y > 5, got %d", newY)
	}
}

func TestDrawConnectors_SameColumn(t *testing.T) {
	// Single source → single target at same column - simple stem + arrow path.
	var c canvas
	c.init(50, 20)
	stepMap := map[string]zenflow.Step{
		"a": {ID: "a"},
		"b": {ID: "b", DependsOn: []string{"a"}},
	}
	boxCenters := map[string]int{"a": 15, "b": 15}
	newY := drawConnectors(&c, 5, []string{"a"}, []string{"b"}, boxCenters, stepMap)
	if newY != 7 {
		t.Errorf("expected y=7 (stem + arrow), got %d", newY)
	}
}

func assertContains(t *testing.T, s, sub string) {
	t.Helper()
	if !contains(s, sub) {
		t.Errorf("expected output to contain %q, got:\n%s", sub, s)
	}
}

func assertNotContains(t *testing.T, s, sub string) {
	t.Helper()
	if contains(s, sub) {
		t.Errorf("expected output to NOT contain %q, got:\n%s", sub, s)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && containsStr(s, sub)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
