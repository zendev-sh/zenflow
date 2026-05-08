package exec

import "testing"

func TestTokenBudgetTransformer_TruncatesContent(t *testing.T) {
	tr := &TokenBudgetTransformer{MaxBytesPerDep: 100}
	longContent := make([]byte, 200)
	for i := range longContent {
		longContent[i] = 'x'
	}
	content, result := tr.TransformStepOutput("s1", string(longContent), nil, "test-model")
	if len(content) > 100+len(truncationMarker) {
		t.Errorf("content length = %d, want <= %d", len(content), 100+len(truncationMarker))
	}
	if result != nil {
		t.Errorf("result should be nil when input is nil")
	}
}

func TestTokenBudgetTransformer_PreservesSmallContent(t *testing.T) {
	tr := &TokenBudgetTransformer{MaxBytesPerDep: 1000}
	content, _ := tr.TransformStepOutput("s1", "short text", nil, "test-model")
	if content != "short text" {
		t.Errorf("content = %q, want %q", content, "short text")
	}
}

func TestTokenBudgetTransformer_TruncatesLargeResult(t *testing.T) {
	tr := &TokenBudgetTransformer{MaxBytesPerDep: 100}
	largeResult := map[string]any{
		"small_field": "ok",
		"large_field": make([]byte, 200),
	}
	_, result := tr.TransformStepOutput("s1", "", largeResult, "test-model")
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if _, ok := result["_truncated"]; !ok {
		t.Error("result should have _truncated marker")
	}
	// Small scalar field should be preserved.
	if _, ok := result["small_field"]; !ok {
		t.Error("small_field should be preserved in truncated result")
	}
}

func TestTokenBudgetTransformer_DefaultBudget(t *testing.T) {
	tr := &TokenBudgetTransformer{} // MaxBytesPerDep = 0 → uses default
	content, _ := tr.TransformStepOutput("s1", "short", nil, "test-model")
	if content != "short" {
		t.Errorf("content = %q, want %q", content, "short")
	}
}

func TestWithOutputTransform(t *testing.T) {
	tr := &TokenBudgetTransformer{MaxBytesPerDep: 1024}
	orch := New(WithOutputTransform(tr))
	if orch.outputTransform != tr {
		t.Error("WithOutputTransform did not set transformer")
	}
}
