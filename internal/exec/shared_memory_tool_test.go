package exec

import (
	"encoding/json"
	"testing"

	"github.com/zendev-sh/goai"
)

func TestSharedMemoryTools_WriteThenRead(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test-agent")

	// Find write tool.
	var writeTool, readTool *goai.Tool
	for i := range tools {
		switch tools[i].Name {
		case "shared_memory_write":
			writeTool = &tools[i]
		case "shared_memory_read":
			readTool = &tools[i]
		}
	}
	if writeTool == nil || readTool == nil {
		t.Fatal("missing shared_memory_write or shared_memory_read tool")
	}

	// Write a key.
	writeArgs, _ := json.Marshal(map[string]string{"key": "api-contract", "value": `{"endpoint": "/users"}`})
	writeResult, err := writeTool.Execute(t.Context(), writeArgs)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if writeResult == "" {
		t.Error("expected non-empty write result")
	}

	// Read the key back.
	readArgs, _ := json.Marshal(map[string]string{"key": "test-agent/api-contract"})
	readResult, err := readTool.Execute(t.Context(), readArgs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readResult != `{"endpoint": "/users"}` {
		t.Errorf("content = %q, want JSON", readResult)
	}
}

func TestSharedMemoryTools_ReadMissing(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test-agent")

	var readTool *goai.Tool
	for i := range tools {
		if tools[i].Name == "shared_memory_read" {
			readTool = &tools[i]
		}
	}

	readArgs, _ := json.Marshal(map[string]string{"key": "nonexistent/key"})
	_, err := readTool.Execute(t.Context(), readArgs)
	// Missing key returns error.
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestSharedMemoryTools_CrossAgentRead(t *testing.T) {
	sm := NewSharedMemory()

	// Agent A writes.
	toolsA := NewSharedMemoryTools(sm, "agent-a")
	var writeToolA *goai.Tool
	for i := range toolsA {
		if toolsA[i].Name == "shared_memory_write" {
			writeToolA = &toolsA[i]
		}
	}
	writeArgs, _ := json.Marshal(map[string]string{"key": "shared-data", "value": "from-a"})
	if _, err := writeToolA.Execute(t.Context(), writeArgs); err != nil {
		t.Fatal(err)
	}

	// Agent B reads agent-a's key.
	toolsB := NewSharedMemoryTools(sm, "agent-b")
	var readToolB *goai.Tool
	for i := range toolsB {
		if toolsB[i].Name == "shared_memory_read" {
			readToolB = &toolsB[i]
		}
	}
	readArgs, _ := json.Marshal(map[string]string{"key": "agent-a/shared-data"})
	result, err := readToolB.Execute(t.Context(), readArgs)
	if err != nil {
		t.Fatal(err)
	}
	if result != "from-a" {
		t.Errorf("content = %q, want from-a", result)
	}
}

func TestSharedMemoryTools_HasBothToolsFromToolTest(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test")

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	names := map[string]bool{}
	for _, d := range tools {
		names[d.Name] = true
	}
	if !names["shared_memory_read"] {
		t.Error("missing shared_memory_read")
	}
	if !names["shared_memory_write"] {
		t.Error("missing shared_memory_write")
	}
}

func TestSharedMemoryTools_WriteInvalidArgs(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test")

	var writeTool *goai.Tool
	for i := range tools {
		if tools[i].Name == "shared_memory_write" {
			writeTool = &tools[i]
		}
	}

	_, err := writeTool.Execute(t.Context(), []byte(`invalid`))
	if err == nil {
		t.Error("expected error for invalid args")
	}
}

func TestSharedMemoryTools_ReadInvalidArgs(t *testing.T) {
	sm := NewSharedMemory()
	tools := NewSharedMemoryTools(sm, "test")

	var readTool *goai.Tool
	for i := range tools {
		if tools[i].Name == "shared_memory_read" {
			readTool = &tools[i]
		}
	}

	_, err := readTool.Execute(t.Context(), []byte(`invalid`))
	if err == nil {
		t.Error("expected error for invalid args on read")
	}
}
