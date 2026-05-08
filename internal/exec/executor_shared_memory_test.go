package exec

import (
	"encoding/json"
	"testing"

	"github.com/zendev-sh/goai"
)

// tests: SharedMemory tools as []goai.Tool

func TestSharedMemoryTools_Write(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test")

	// Find the write tool.
	var writeTool *goai.Tool
	for i := range tools {
		if tools[i].Name == "shared_memory_write" {
			writeTool = &tools[i]
			break
		}
	}
	if writeTool == nil {
		t.Fatal("shared_memory_write tool not found")
	}

	// Execute write.
	args, _ := json.Marshal(map[string]string{"key": "k1", "value": "v1"})
	result, err := writeTool.Execute(t.Context(), args)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	// Verify the value was written.
	val, ok := sm.Read("test/k1")
	if !ok || val != "v1" {
		t.Errorf("sm.Read('test/k1') = %q, %v; want 'v1', true", val, ok)
	}
}

func TestSharedMemoryTools_Read(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("test", "k1", "v1")

	tools := NewSharedMemoryTools(sm, "test")

	// Find the read tool.
	var readTool *goai.Tool
	for i := range tools {
		if tools[i].Name == "shared_memory_read" {
			readTool = &tools[i]
			break
		}
	}
	if readTool == nil {
		t.Fatal("shared_memory_read tool not found")
	}

	// Execute read.
	args, _ := json.Marshal(map[string]string{"key": "test/k1"})
	result, err := readTool.Execute(t.Context(), args)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result != "v1" {
		t.Errorf("result = %q, want 'v1'", result)
	}
}

func TestSharedMemoryTools_HasBothTools(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test")

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["shared_memory_read"] {
		t.Error("missing shared_memory_read")
	}
	if !names["shared_memory_write"] {
		t.Error("missing shared_memory_write")
	}
}
