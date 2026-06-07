package exec

import (
	"fmt"

	"github.com/zendev-sh/goai"
)

// validateToolNames returns an error if any two tools share the same name.
// Duplicate tool names silently override each other in goai (last-wins),
// making bugs very hard to trace. This check runs after all tool assembly
// (user-supplied tools, auto-injected send_message, auto-injected
// submit_result) so every source of duplicates is caught.
func validateToolNames(tools []goai.Tool) error {
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if _, exists := seen[t.Name]; exists {
			return fmt.Errorf("zenflow: duplicate tool name %q: tool names must be unique", t.Name)
		}
		seen[t.Name] = struct{}{}
	}
	return nil
}
