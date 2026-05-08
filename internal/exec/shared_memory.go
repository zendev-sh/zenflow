package exec

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// SharedMemory provides namespaced key-value storage for inter-agent collaboration.
// Keys are qualified as "agentName/key". Agents write to their own namespace
// but can read any namespace.
// Stable.
type SharedMemory struct {
	mu      sync.RWMutex
	entries map[string]string // "agentName/key" -> value
}

// NewSharedMemory creates a new empty SharedMemory.
func NewSharedMemory() *SharedMemory {
	return &SharedMemory{
		entries: make(map[string]string),
	}
}

// Write stores a value at "agent/key", replacing any existing value.
// Agent names must not contain "/" to avoid namespace collisions.
func (sm *SharedMemory) Write(agent, key, value string) {
	if strings.Contains(agent, "/") {
 // Silently sanitize: replace "/" with "_" to prevent namespace collision.
		agent = strings.ReplaceAll(agent, "/", "_")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.entries[agent+"/"+key] = value
}

// Read returns the value for a fully qualified "agent/key" and whether it exists.
func (sm *SharedMemory) Read(qualifiedKey string) (string, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	v, ok := sm.entries[qualifiedKey]
	return v, ok
}

// ListByAgent returns all entries for the given agent, with keys stripped of the
// namespace prefix.
func (sm *SharedMemory) ListByAgent(agent string) map[string]string {
	prefix := agent + "/"
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]string, len(sm.entries))
	for k, v := range sm.entries {
		if strings.HasPrefix(k, prefix) {
			result[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return result
}

// Summary returns a markdown digest of all entries for context injection.
// Each entry is formatted as "- agent/key: value" (truncated to 100 chars).
func (sm *SharedMemory) Summary() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.entries) == 0 {
		return ""
	}

	// Sort keys for deterministic output.
	keys := slices.Sorted(maps.Keys(sm.entries))

	var b strings.Builder
	for _, k := range keys {
		v := sm.entries[k]
		runes := []rune(v)
		if len(runes) > 100 {
			v = string(runes[:100]) + "..."
		}
		fmt.Fprintf(&b, "- %s: %s\n", k, v)
	}
	return b.String()
}

// Entries returns a shallow copy of all entries (for persistence via Storage).
func (sm *SharedMemory) Entries() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return maps.Clone(sm.entries)
}

// LoadEntries bulk-loads entries from Storage (for resume). Existing entries are
// replaced.
func (sm *SharedMemory) LoadEntries(entries map[string]string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for k, v := range entries {
		sm.entries[k] = v
	}
}
