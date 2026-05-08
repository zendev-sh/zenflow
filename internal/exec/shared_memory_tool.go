package exec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zendev-sh/goai"
)

// NewSharedMemoryTools returns goai.Tool values that read/write to sm
// under the given agentName namespace.
func NewSharedMemoryTools(sm *SharedMemory, agentName string) []goai.Tool {
	return []goai.Tool{
		{
			Name:        "shared_memory_write",
			Description: "Write a key-value pair to shared memory. The key will be namespaced under your agent name.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"Key name (will be namespaced under your agent name)"},"value":{"type":"string","description":"Value to store"}},"required":["key","value"]}`),
			Execute: func(_ context.Context, args json.RawMessage) (string, error) {
				var p struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return "", fmt.Errorf("shared_memory_write: invalid arguments: %w", err)
				}
				sm.Write(agentName, p.Key, p.Value)
				return "ok", nil
			},
		},
		{
			Name:        "shared_memory_read",
			Description: "Read a value from shared memory by fully qualified key.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","description":"Fully qualified key in 'agent/key' format"}},"required":["key"]}`),
			Execute: func(_ context.Context, args json.RawMessage) (string, error) {
				var p struct {
					Key string `json:"key"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return "", fmt.Errorf("shared_memory_read: invalid arguments: %w", err)
				}
				val, ok := sm.Read(p.Key)
				if !ok {
					return "", fmt.Errorf("shared_memory_read: key %q not found", p.Key)
				}
				return val, nil
			},
		},
	}
}
