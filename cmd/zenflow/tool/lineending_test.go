package tool

// lineending_test.go -  evidence.
// The contract: read and write are byte-perfect - they do NOT translate
// CRLF↔LF in either direction. A Windows file authored with CRLF
// survives a read+write round-trip with its line endings intact, and
// a Unix file with LF stays LF on Windows.
// This is a deliberate non-translation: an LLM editing source files
// must see what's actually on disk, otherwise diffs and version
// control behaviour become unpredictable.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadWrite_PreservesCRLF writes CRLF content via the write tool,
// reads it back via the read tool, and asserts the bytes are identical.
// Run on every OS - the CRLF passthrough is a property of our code
// (not the OS), so the assertion holds the same way on macOS and on
// Windows CI.
func TestReadWrite_PreservesCRLF(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "crlf.txt")
	original := "line one\r\nline two\r\nline three\r\n"

	w := writeTool()
	wargs, _ := json.Marshal(map[string]string{"path": tmp, "content": original})
	if _, err := w.Execute(context.Background(), wargs); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Sanity: bytes on disk match exactly.
	onDisk, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if !bytes.Equal(onDisk, []byte(original)) {
		t.Fatalf("on-disk bytes differ from input. got=%q want=%q", onDisk, original)
	}

	r := readTool()
	rargs, _ := json.Marshal(map[string]string{"path": tmp})
	out, err := r.Execute(context.Background(), rargs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != original {
		t.Fatalf("read output differs. got=%q want=%q", out, original)
	}
	// Belt-and-braces: count CRLF occurrences explicitly so a partial
	// translation (e.g. only the trailing line ending stripped) shows up.
	if got := strings.Count(out, "\r\n"); got != 3 {
		t.Errorf("CRLF count = %d, want 3", got)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\r") {
		t.Errorf("output contains stray \\r outside CRLF pairs: %q", out)
	}
}

// TestReadWrite_PreservesLF asserts the symmetric Unix case - LF-only
// content is not silently converted to CRLF on round-trip.
func TestReadWrite_PreservesLF(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "lf.txt")
	original := "line one\nline two\nline three\n"

	w := writeTool()
	wargs, _ := json.Marshal(map[string]string{"path": tmp, "content": original})
	if _, err := w.Execute(context.Background(), wargs); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := readTool()
	rargs, _ := json.Marshal(map[string]string{"path": tmp})
	out, err := r.Execute(context.Background(), rargs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != original {
		t.Fatalf("read output differs. got=%q want=%q", out, original)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("LF-only round-trip introduced \\r: %q", out)
	}
}

// TestReadWrite_PreservesMixed asserts that mixed line endings - a
// real-world hazard when an editor saves a file inconsistently - also
// survive untouched.
func TestReadWrite_PreservesMixed(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "mixed.txt")
	original := "crlf\r\nlf\nfinal-crlf\r\n"

	w := writeTool()
	wargs, _ := json.Marshal(map[string]string{"path": tmp, "content": original})
	if _, err := w.Execute(context.Background(), wargs); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := readTool()
	rargs, _ := json.Marshal(map[string]string{"path": tmp})
	out, err := r.Execute(context.Background(), rargs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != original {
		t.Fatalf("mixed round-trip differs. got=%q want=%q", out, original)
	}
}
