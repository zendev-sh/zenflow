package exec

import "encoding/json"

// TokenBudgetTransformer truncates step output to a fixed byte budget.
// This is the default CLI transformer (P7.7.7) that prevents context overflow
// on models with smaller context windows (e.g., MiniMax 196K tokens).
type TokenBudgetTransformer struct {
	// MaxBytesPerDep caps each dependency's content size in bytes.
	// If 0, defaults to maxDepContentBytes (16KB).
	MaxBytesPerDep int
}

// - compile-time assertion that *TokenBudgetTransformer
// satisfies the OutputTransformer contract.
var _ OutputTransformer = (*TokenBudgetTransformer)(nil)

// TransformStepOutput truncates content and serialized result to the byte budget.
func (t *TokenBudgetTransformer) TransformStepOutput(stepID string, content string, result map[string]any, targetModel string) (string, map[string]any) {
	budget := t.MaxBytesPerDep
	if budget <= 0 {
		budget = maxDepContentBytes
	}

	content = truncateForContext(content, budget)

	// Truncate serialized result if it exceeds budget.
	if result != nil {
		resultJSON, err := json.Marshal(result)
		if err == nil && len(resultJSON) > budget {
			// Return a simplified result with just the keys to signal truncation.
			simplified := make(map[string]any, 1)
			simplified["_truncated"] = true
			simplified["_note"] = "Result was too large and has been truncated. Key fields may be missing."
			// Try to preserve small scalar fields.
			for k, v := range result {
				vJSON, _ := json.Marshal(v)
				if len(vJSON) < 1024 {
					simplified[k] = v
				}
			}
			result = simplified
		}
	}

	return content, result
}
