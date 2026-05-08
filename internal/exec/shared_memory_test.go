package exec

import (
	"strings"
	"testing"
)

func TestSharedMemory_WriteRead(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("backend", "api-contract", `{"endpoint": "/users"}`)
	val, ok := sm.Read("backend/api-contract")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if val != `{"endpoint": "/users"}` {
		t.Errorf("value = %q, want JSON", val)
	}
}

func TestSharedMemory_ReadMissing(t *testing.T) {
	sm := NewSharedMemory()
	_, ok := sm.Read("nonexistent/key")
	if ok {
		t.Error("expected false for missing key")
	}
}

func TestSharedMemory_Namespace(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("agent-a", "key1", "val-a")
	sm.Write("agent-b", "key1", "val-b")

	valA, _ := sm.Read("agent-a/key1")
	valB, _ := sm.Read("agent-b/key1")

	if valA != "val-a" {
		t.Errorf("agent-a/key1 = %q, want val-a", valA)
	}
	if valB != "val-b" {
		t.Errorf("agent-b/key1 = %q, want val-b", valB)
	}
}

func TestSharedMemory_ListByAgent(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("backend", "key1", "v1")
	sm.Write("backend", "key2", "v2")
	sm.Write("frontend", "key3", "v3")

	backendKeys := sm.ListByAgent("backend")
	if len(backendKeys) != 2 {
		t.Errorf("expected 2 backend keys, got %d", len(backendKeys))
	}
	if backendKeys["key1"] != "v1" || backendKeys["key2"] != "v2" {
		t.Errorf("backend keys = %v", backendKeys)
	}

	frontendKeys := sm.ListByAgent("frontend")
	if len(frontendKeys) != 1 {
		t.Errorf("expected 1 frontend key, got %d", len(frontendKeys))
	}
}

func TestSharedMemory_Summary(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("backend", "api", "REST")
	sm.Write("frontend", "framework", "React")

	summary := sm.Summary()
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	// Summary should mention the keys.
	if !strings.Contains(summary, "backend/api") || !strings.Contains(summary, "frontend/framework") {
		t.Errorf("summary missing keys: %s", summary)
	}
}

func TestSharedMemory_SlashInAgentName(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("sub/agent", "key", "val")

	// Slash should be sanitized to underscore.
	v, ok := sm.Read("sub_agent/key")
	if !ok {
		t.Fatal("expected key under sanitized agent name")
	}
	if v != "val" {
		t.Errorf("value = %q, want val", v)
	}

	// Original agent/key format should NOT exist.
	_, ok = sm.Read("sub/agent/key")
	if ok {
		t.Error("unsanitized agent/key should not exist")
	}
}

func TestSharedMemory_Overwrite(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("agent", "key", "old")
	sm.Write("agent", "key", "new")

	val, ok := sm.Read("agent/key")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if val != "new" {
		t.Errorf("value = %q, want new", val)
	}
}

func TestSharedMemory_Entries(t *testing.T) {
	sm := NewSharedMemory()

	sm.Write("a", "k1", "v1")
	sm.Write("b", "k2", "v2")

	entries := sm.Entries()
	if entries["a/k1"] != "v1" || entries["b/k2"] != "v2" {
		t.Errorf("entries = %v", entries)
	}
}

func TestSharedMemory_LoadEntries(t *testing.T) {
	sm := NewSharedMemory()
	sm.LoadEntries(map[string]string{
		"agent-x/key1": "val1",
		"agent-y/key2": "val2",
	})

	v1, ok := sm.Read("agent-x/key1")
	if !ok || v1 != "val1" {
		t.Errorf("key1 = %q, %v", v1, ok)
	}
	v2, ok := sm.Read("agent-y/key2")
	if !ok || v2 != "val2" {
		t.Errorf("key2 = %q, %v", v2, ok)
	}
}

func TestSharedMemory_SummaryEmpty(t *testing.T) {
	sm := NewSharedMemory()
	summary := sm.Summary()
	if summary != "" {
		t.Errorf("Summary() = %q, want empty string for no entries", summary)
	}
}

func TestSharedMemory_SummaryTruncation(t *testing.T) {
	sm := NewSharedMemory()
	// Write a value longer than 100 runes to trigger truncation.
	longVal := strings.Repeat("x", 150)
	sm.Write("agent", "key", longVal)

	summary := sm.Summary()
	if !strings.Contains(summary, "...") {
		t.Errorf("Summary should truncate long values with '...': %s", summary)
	}
	// The truncated value should be 100 runes + "..."
	if strings.Contains(summary, strings.Repeat("x", 101)) {
		t.Error("Summary should truncate to 100 runes")
	}
}

func TestSharedMemory_ConcurrentAccess(t *testing.T) {
	sm := NewSharedMemory()
	done := make(chan struct{})

	// Write from multiple goroutines.
	for i := range 10 {
		go func() {
			defer func() { done <- struct{}{} }()
			agent := "agent"
			key := "key" + string(rune('0'+i))
			sm.Write(agent, key, "val")
		}()
	}

	for range 10 {
		<-done
	}

	entries := sm.Entries()
	if len(entries) != 10 {
		t.Errorf("expected 10 entries, got %d", len(entries))
	}
}
