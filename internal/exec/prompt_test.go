package exec

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// : agent.Prompt now flows to goai.WithSystem, not the user-message
// "## Agent Role" header. AssemblePrompt no longer emits that section;
// the user-message asserts focus on the Task / Context / Required
// blocks. The original "BothRoleAndInstructions" test became
// "InstructionsAreInUserMessage" + the role flows through a separate
// system-prompt path verified in executor_step.go E2E.
func TestAssemblePrompt_InstructionsAreInUserMessage(t *testing.T) {
	agent := AgentConfig{Prompt: "You are a senior engineer."}
	step := Step{Instructions: "Review the pull request for correctness."}

	got, _ := AssemblePrompt(agent, step, "", nil)

	// agent.Prompt no longer leaks into the user message - it lives
	// in the system slot now. The ONLY content we expect here is the
	// Task header + instructions.
	if strings.Contains(got, "You are a senior engineer.") {
		t.Error("agent.Prompt leaked into user-message; should be system-only after Z.7.4")
	}
	if strings.Contains(got, "## Agent Role") {
		t.Error("'## Agent Role' header should not appear after Z.7.4 migration")
	}
	if !strings.Contains(got, "## Task") {
		t.Error("missing '## Task' section")
	}
	if !strings.Contains(got, "Review the pull request for correctness.") {
		t.Error("missing instructions content")
	}
}

func TestAssemblePrompt_OnlyInstructions(t *testing.T) {
	agent := AgentConfig{} // No prompt.
	step := Step{Instructions: "Design the API."}

	got, _ := AssemblePrompt(agent, step, "", nil)

	if strings.Contains(got, "## Agent Role") {
		t.Error("should not have '## Agent Role' when no prompt")
	}
	if !strings.Contains(got, "## Task") {
		t.Error("missing '## Task' section")
	}
	if !strings.Contains(got, "Design the API.") {
		t.Error("missing instructions content")
	}
}

func TestAssemblePrompt_OnlyRoleProducesEmptyUserMessage(t *testing.T) {
	// Post-: when only agent.Prompt is set, the user message is
	// empty (no "## Agent Role" section, no Task section). The role
	// flows to system via executor_step.go's runner construction.
	agent := AgentConfig{Prompt: "You are an architect."}
	step := Step{} // No instructions.

	got, _ := AssemblePrompt(agent, step, "", nil)

	if strings.Contains(got, "## Agent Role") {
		t.Error("'## Agent Role' header should not appear after Z.7.4 migration")
	}
	if strings.Contains(got, "You are an architect.") {
		t.Error("agent.Prompt leaked into user-message after Z.7.4 migration")
	}
	if strings.Contains(got, "## Task") {
		t.Error("should not have '## Task' when no instructions")
	}
}

func TestAssemblePrompt_WithContextFiles(t *testing.T) {
	agent := AgentConfig{Prompt: "You are a coder."}
	step := Step{
		Instructions: "Implement the feature.",
		ContextFiles: []string{"main.go", "config.yaml", "README.md"},
	}

	got, _ := AssemblePrompt(agent, step, "", nil)

	if !strings.Contains(got, "## Context Files") {
		t.Error("missing '## Context Files' section")
	}
	for _, f := range step.ContextFiles {
		if !strings.Contains(got, "### "+f) {
			t.Errorf("missing context file heading %q in output", f)
		}
	}
	// Non-existent files should produce error messages.
	if !strings.Contains(got, "(error reading file:") {
		t.Error("expected error messages for non-existent context files")
	}

	// Context Files should come after Task.
	taskIdx := strings.Index(got, "## Task")
	ctxIdx := strings.Index(got, "## Context Files")
	if ctxIdx <= taskIdx {
		t.Errorf("context files (pos %d) should come after task (pos %d)", ctxIdx, taskIdx)
	}
}

func TestAssemblePrompt_WithRealContextFile(t *testing.T) {
	// Use a file that exists in the repo.
	agent := AgentConfig{}
	step := Step{
		ContextFiles: []string{"prompt.go"},
	}

	got, _ := AssemblePrompt(agent, step, "", nil)

	if !strings.Contains(got, "### prompt.go") {
		t.Error("missing context file heading")
	}
	// The file content should include its own package declaration.
	if !strings.Contains(got, "package exec") {
		t.Error("expected file contents to be included")
	}
}

func TestAssemblePrompt_Empty(t *testing.T) {
	agent := AgentConfig{}
	step := Step{}

	got, _ := AssemblePrompt(agent, step, "", nil)

	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestAssemblePrompt_WithPriorResults(t *testing.T) {
	agent := AgentConfig{Prompt: "You are a reviewer."}
	step := Step{
		Instructions: "Review the code.",
		DependsOn:    []string{"design", "implement"},
	}
	priorResults := map[string]*StepResult{
		"design":    {ID: "design", Status: spec.StepCompleted, Content: "Design document v1"},
		"implement": {ID: "implement", Status: spec.StepCompleted, Content: "Implementation complete"},
	}

	got, _ := AssemblePrompt(agent, step, "", priorResults)

	if !strings.Contains(got, "## Previous Step Results") {
		t.Error("missing '## Previous Step Results' section")
	}
	if !strings.Contains(got, "### design (completed)") {
		t.Error("missing design result heading")
	}
	if !strings.Contains(got, "Design document v1") {
		t.Error("missing design output")
	}
	if !strings.Contains(got, "### implement (completed)") {
		t.Error("missing implement result heading")
	}
	if !strings.Contains(got, "Implementation complete") {
		t.Error("missing implement output")
	}

	// Previous Step Results should come after Task and before Context Files.
	taskIdx := strings.Index(got, "## Task")
	priorIdx := strings.Index(got, "## Previous Step Results")
	if priorIdx <= taskIdx {
		t.Errorf("prior results (pos %d) should come after task (pos %d)", priorIdx, taskIdx)
	}
}

func TestAssemblePrompt_WithPriorResults_OnlyDeps(t *testing.T) {
	agent := AgentConfig{}
	step := Step{
		Instructions: "Final step.",
		DependsOn:    []string{"step1"},
	}
	// priorResults contains step1 (a dep) and step2 (NOT a dep).
	priorResults := map[string]*StepResult{
		"step1": {ID: "step1", Status: spec.StepCompleted, Content: "output from step1"},
		"step2": {ID: "step2", Status: spec.StepCompleted, Content: "output from step2"},
	}

	got, _ := AssemblePrompt(agent, step, "", priorResults)

	if !strings.Contains(got, "output from step1") {
		t.Error("missing output from dep step1")
	}
	if strings.Contains(got, "output from step2") {
		t.Error("should NOT include output from non-dep step2")
	}
	if strings.Contains(got, "### step2") {
		t.Error("should NOT include heading for non-dep step2")
	}
}

func TestAssemblePrompt_FailedDependencyExcluded(t *testing.T) {
	agent := AgentConfig{Prompt: "You are a reviewer."}
	step := Step{
		Instructions: "Summarize results.",
		DependsOn:    []string{"step-ok", "step-fail"},
	}
	priorResults := map[string]*StepResult{
		"step-ok":   {ID: "step-ok", Status: spec.StepCompleted, Content: "success output"},
		"step-fail": {ID: "step-fail", Status: spec.StepFailed, Content: "should not appear"},
	}

	got, _ := AssemblePrompt(agent, step, "", priorResults)

	if !strings.Contains(got, "success output") {
		t.Error("missing completed dep output")
	}
	if strings.Contains(got, "should not appear") {
		t.Error("failed dep output should be excluded from prompt")
	}
	if strings.Contains(got, "### step-fail") {
		t.Error("failed dep heading should be excluded from prompt")
	}
}

func TestAssemblePrompt_ContextFile_PathTraversalBlocked(t *testing.T) {
	// Trigger the path-escape check in AssemblePromptWithForEach (line 106-108).
	dir := t.TempDir()
	agent := AgentConfig{}
	step := Step{
		ContextFiles: []string{"../../etc/passwd"},
	}
	got, _ := AssemblePrompt(agent, step, dir, nil)
	if !strings.Contains(got, "(error: path escapes workflow directory)") {
		t.Errorf("expected path escape error, got:\n%s", got)
	}
}

func TestAssemblePromptWithForEach_JsonMarshalError(t *testing.T) {
	// Trigger the json.Marshal error path for forEach item (line 134-136 in prompt.go).
	// A channel cannot be marshalled to JSON.
	agent := AgentConfig{}
	step := Step{Instructions: "process item"}
	fe := &ForEachContext{Item: make(chan int), Index: 0}
	got, _ := AssemblePromptWithForEach(agent, step, "", nil, fe)
	if !strings.Contains(got, "## forEach Item (index: 0)") {
		t.Error("expected forEach Item header")
	}
	// When json.Marshal fails, it falls back to fmt.Sprintf("%v", item).
	// The fallback will contain the channel address (0x...) - just verify
	// the forEach header is present and that there's no JSON object.
	if strings.Contains(got, `"`) && strings.Contains(got, `{`) {
		t.Error("expected fallback format (not JSON)")
	}
}

func TestAssemblePrompt_NoPriorResults(t *testing.T) {
	agent := AgentConfig{Prompt: "You are a coder."}
	step := Step{Instructions: "Write code."}

	// No deps, no prior results.
	got, _ := AssemblePrompt(agent, step, "", nil)

	if strings.Contains(got, "## Previous Step Results") {
		t.Error("should NOT have Previous Step Results section when no deps")
	}

	// Also test with empty map.
	got2, _ := AssemblePrompt(agent, step, "", map[string]*StepResult{})
	if strings.Contains(got2, "## Previous Step Results") {
		t.Error("should NOT have Previous Step Results section with empty map")
	}
}

func TestAssemblePrompt_ImageAttachment(t *testing.T) {
	// Create a temp PNG file.
	dir := t.TempDir()
	pngData := []byte{0x89, 'P', 'N', 'G'} // minimal fake PNG data
	if err := os.WriteFile(filepath.Join(dir, "test.png"), pngData, 0o644); err != nil {
		t.Fatal(err)
	}

	step := Step{
		Instructions: "Describe the image.",
		ContextFiles: []string{"test.png"},
	}

	got, attachments := AssemblePrompt(AgentConfig{}, step, dir, nil)

	// Text should mention the file as attached.
	if !strings.Contains(got, "(attached as multimodal content)") {
		t.Errorf("expected 'attached as multimodal content' in text, got:\n%s", got)
	}

	// Should have one image attachment.
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].Type != provider.PartImage {
		t.Errorf("attachment type = %q, want PartImage", attachments[0].Type)
	}
	if !strings.HasPrefix(attachments[0].URL, "data:image/png;base64,") {
		t.Errorf("attachment URL = %q, want data URI prefix", attachments[0].URL)
	}
}

func TestAssemblePrompt_PDFAttachment(t *testing.T) {
	dir := t.TempDir()
	pdfData := []byte("%PDF-1.0 test")
	if err := os.WriteFile(filepath.Join(dir, "doc.pdf"), pdfData, 0o644); err != nil {
		t.Fatal(err)
	}

	step := Step{
		Instructions: "Read the PDF.",
		ContextFiles: []string{"doc.pdf"},
	}

	got, attachments := AssemblePrompt(AgentConfig{}, step, dir, nil)

	if !strings.Contains(got, "(attached as multimodal content)") {
		t.Errorf("expected attachment marker in text, got:\n%s", got)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].Type != provider.PartFile {
		t.Errorf("attachment type = %q, want PartFile", attachments[0].Type)
	}
	if attachments[0].MediaType != "application/pdf" {
		t.Errorf("media type = %q, want application/pdf", attachments[0].MediaType)
	}
	if attachments[0].Filename != "doc.pdf" {
		t.Errorf("filename = %q, want doc.pdf", attachments[0].Filename)
	}
}

func TestAssemblePrompt_MixedContextFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte{0xFF, 0xD8}, 0o644)
	os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("%PDF"), 0o644)

	step := Step{
		Instructions: "Analyze all files.",
		ContextFiles: []string{"readme.md", "photo.jpg", "report.pdf"},
	}

	got, attachments := AssemblePrompt(AgentConfig{}, step, dir, nil)

	// Text file inlined.
	if !strings.Contains(got, "# Hello") {
		t.Error("expected text file content inlined")
	}
	// 2 binary attachments (jpg + pdf).
	if len(attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(attachments))
	}
	if attachments[0].Type != provider.PartImage {
		t.Errorf("first attachment type = %q, want PartImage", attachments[0].Type)
	}
	if attachments[1].Type != provider.PartFile {
		t.Errorf("second attachment type = %q, want PartFile", attachments[1].Type)
	}
}

func TestAssemblePrompt_BinaryFileReadError(t *testing.T) {
	step := Step{
		Instructions: "Analyze.",
		ContextFiles: []string{"nonexistent.png"},
	}

	got, attachments := AssemblePrompt(AgentConfig{}, step, t.TempDir(), nil)

	// Should contain an error (either read error or path error).
	if !strings.Contains(got, "(error") {
		t.Errorf("expected error message for missing image file, got:\n%s", got)
	}
	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments for missing file, got %d", len(attachments))
	}
}

func TestAssemblePrompt_BinaryFileReadError_NoBaseDir(t *testing.T) {
	// Without baseDir, no path traversal check - goes straight to ReadFile.
	step := Step{
		Instructions: "Analyze.",
		ContextFiles: []string{"nonexistent.png"},
	}

	got, attachments := AssemblePrompt(AgentConfig{}, step, "", nil)

	if !strings.Contains(got, "(error reading file:") {
		t.Errorf("expected read error, got:\n%s", got)
	}
	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestMediaTypeForExt(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		want string
	}{
		{"png", ".png", "image/png"},
		{"jpg", ".jpg", "image/jpeg"},
		{"jpeg", ".jpeg", "image/jpeg"},
		{"gif", ".gif", "image/gif"},
		{"webp", ".webp", "image/webp"},
		{"pdf", ".pdf", "application/pdf"},
		{"go", ".go", ""},
		{"txt", ".txt", ""},
		{"md", ".md", ""},
		{"yaml", ".yaml", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := mediaTypeForExt(tt.ext); got != tt.want {
				t.Errorf("mediaTypeForExt(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}

func TestIsImageMediaType(t *testing.T) {
	if !isImageMediaType("image/png") {
		t.Error("image/png should be image")
	}
	if isImageMediaType("application/pdf") {
		t.Error("application/pdf should not be image")
	}
	if isImageMediaType("") {
		t.Error("empty should not be image")
	}
}

// TestAssemblePrompt_OversizedImageAttachment covers prompt.go:229-233 -
// an image/PDF context file larger than MaxAttachmentSizeBytes is
// reported in the prompt with the "exceeds N MiB attachment cap" error
// and skipped (no read, no Part appended).
func TestAssemblePrompt_OversizedImageAttachment(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "huge.png")
	// Sparse-truncate to MaxAttachmentSizeBytes+1 - instant on most fs.
	f, err := os.Create(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(MaxAttachmentSizeBytes + 1)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	agent := AgentConfig{}
	step := Step{ContextFiles: []string{"huge.png"}}
	got, parts := AssemblePrompt(agent, step, dir, nil)
	if !strings.Contains(got, "huge.png") {
		t.Error("expected filename header in prompt")
	}
	if !strings.Contains(got, "attachment cap") {
		t.Errorf("prompt missing oversize error; got %q", got)
	}
	if len(parts) != 0 {
		t.Errorf("expected no multimodal parts for oversized file, got %d", len(parts))
	}
}

// TestAssemblePrompt_OversizedTextAttachment covers prompt.go:278-280 -
// a text context file larger than MaxAttachmentSizeBytes is reported
// with the same cap error and skipped (no inlining).
func TestAssemblePrompt_OversizedTextAttachment(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "huge.txt")
	f, err := os.Create(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(MaxAttachmentSizeBytes + 1)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	agent := AgentConfig{}
	step := Step{ContextFiles: []string{"huge.txt"}}
	got, _ := AssemblePrompt(agent, step, dir, nil)
	if !strings.Contains(got, "attachment cap") {
		t.Errorf("prompt missing oversize error; got %q", got)
	}
}

// TestAssemblePrompt_ImageReadFileError covers prompt.go:237-243 -
// Stat reports a file under the cap but ReadFile fails. We trigger this
// with a mode-0 file: Stat succeeds, ReadFile fails with EACCES.
func TestAssemblePrompt_ImageReadFileError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores POSIX file modes; chmod 0 still permits reads")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root: chmod 0 doesn't deny reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "no-read.png")
	if err := os.WriteFile(path, []byte("PNG\x00"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	agent := AgentConfig{}
	step := Step{ContextFiles: []string{"no-read.png"}}
	got, parts := AssemblePrompt(agent, step, dir, nil)
	if !strings.Contains(got, "error reading file") {
		t.Errorf("prompt missing read-error message; got %q", got)
	}
	if len(parts) != 0 {
		t.Errorf("expected no multimodal parts after read error, got %d", len(parts))
	}
}

// TestAssemblePrompt_TextReadFileError covers prompt.go:283-287 -
// Stat passes for the text file but ReadFile fails (mode 0).
func TestAssemblePrompt_TextReadFileError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ignores POSIX file modes; chmod 0 still permits reads")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root: chmod 0 doesn't deny reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "no-read.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	agent := AgentConfig{}
	step := Step{ContextFiles: []string{"no-read.txt"}}
	got, _ := AssemblePrompt(agent, step, dir, nil)
	if !strings.Contains(got, "error reading file") {
		t.Errorf("prompt missing read-error message; got %q", got)
	}
}
