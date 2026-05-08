package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zendev-sh/goai"
)

func findTool(tools []goai.Tool, name string) *goai.Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func TestDefaultTools_HasAllTools(t *testing.T) {
	tools := DefaultTools()
	if len(tools) != 5 {
		t.Errorf("got %d tools, want 5", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"bash", "read", "write", "glob", "grep"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

func TestBash_Execute(t *testing.T) {
	tools := DefaultTools()
	bash := findTool(tools, "bash")
	if bash == nil {
		t.Fatal("bash tool not found")
	}
	result, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(result); got != "hello" {
		t.Errorf("result = %q, want %q", got, "hello")
	}
}

func TestRead_Execute(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "read-test.txt")
	if err := os.WriteFile(tmp, []byte("file content"), 0644); err != nil {
		t.Fatal(err)
	}
	tools := DefaultTools()
	readTool := findTool(tools, "read")
	args, _ := json.Marshal(map[string]string{"path": tmp})
	result, err := readTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "file content" {
		t.Errorf("result = %q, want %q", result, "file content")
	}
}

func TestWrite_Execute(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "write-test.txt")
	tools := DefaultTools()
	writeTool := findTool(tools, "write")
	args, _ := json.Marshal(map[string]string{"path": tmp, "content": "written"})
	result, err := writeTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want %q", result, "ok")
	}
	data, _ := os.ReadFile(tmp)
	if string(data) != "written" {
		t.Errorf("file content = %q, want %q", string(data), "written")
	}
}

func TestGlob_Execute(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644)
	}
	tools := DefaultTools()
	globTool := findTool(tools, "glob")
	args, _ := json.Marshal(map[string]string{"pattern": filepath.Join(dir, "*.go")})
	result, err := globTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	sort.Strings(lines)
	if len(lines) != 2 {
		t.Fatalf("got %d matches, want 2", len(lines))
	}
}

func TestGrep_Execute(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world\nfoo bar\nhello again"), 0644)
	tools := DefaultTools()
	grepTool := findTool(tools, "grep")
	args, _ := json.Marshal(map[string]string{"pattern": "hello", "path": dir})
	result, err := grepTool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected 'hello world' in output, got: %s", result)
	}
}

// FilterTools is in the zenflow package (agent_tool.go), tested there.
