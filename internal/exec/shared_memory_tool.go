package exec

import (
	"context"
	"fmt"

	"github.com/zendev-sh/goai"
)

// NewSharedMemoryTools returns goai.Tool values that read/write to sm
// under the given agentName namespace.
func NewSharedMemoryTools(sm *SharedMemory, agentName string) []goai.Tool {
	return []goai.Tool{
		goai.NewTool("shared_memory_write",
			"Write a key-value pair to shared memory. The key will be namespaced under your agent name.",
			func(_ context.Context, p struct {
				Key   string `json:"key" jsonschema:"description=Key name (will be namespaced under your agent name)"`
				Value string `json:"value" jsonschema:"description=Value to store"`
			}) (string, error) {
				sm.Write(agentName, p.Key, p.Value)
				return "ok", nil
			}),
		goai.NewTool("shared_memory_read",
			"Read a value from shared memory by fully qualified key.",
			func(_ context.Context, p struct {
				Key string `json:"key" jsonschema:"description=Fully qualified key in 'agent/key' format"`
			}) (string, error) {
				val, ok := sm.Read(p.Key)
				if !ok {
					return "", fmt.Errorf("shared_memory_read: key %q not found", p.Key)
				}
				return val, nil
			}),
	}
}
