package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadSkills_DirAndFlatForms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "review", "SKILL.md"), `---
name: code-review
description: Review a diff for correctness.
---
# Code Review

Read the diff, then report findings.
`)
	writeFile(t, filepath.Join(dir, "notes.md"), `# Notes

Keep notes short.
`)

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if got := skills.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2 (%v)", got, skills.Names())
	}
	names := skills.Names()
	if names[0] != "code-review" || names[1] != "notes" {
		t.Fatalf("Names = %v, want [code-review notes]", names)
	}
	index := skills.IndexBlock()
	if !strings.Contains(index, "`code-review` — Review a diff for correctness.") {
		t.Errorf("index missing frontmatter entry:\n%s", index)
	}
	if !strings.Contains(index, "`notes` — Notes") {
		t.Errorf("index missing fallback description:\n%s", index)
	}
}

func TestLoadSkills_ParseFrontmatterErrorsIgnored(t *testing.T) {
	dir := t.TempDir()
	// Unterminated frontmatter is treated as body; the name falls back to
	// the file stem.
	writeFile(t, filepath.Join(dir, "broken.md"), "---\nname: x\n# no closing delimiter\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if got := skills.Names(); len(got) != 1 || got[0] != "broken" {
		t.Fatalf("Names = %v, want [broken]", got)
	}
}

func TestLoadSkills_NoSkillsIsError(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSkills(dir); err == nil || !strings.Contains(err.Error(), "no skills found") {
		t.Fatalf("err = %v, want no skills found", err)
	}
}

func TestLoadSkills_MissingDirIsError(t *testing.T) {
	if _, err := LoadSkills(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestLoadSkillsDirs_FirstWinsOnDuplicate(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeFile(t, filepath.Join(first, "shared.md"), "---\nname: shared\ndescription: first\n---\nA\n")
	writeFile(t, filepath.Join(second, "shared.md"), "---\nname: shared\ndescription: second\n---\nB\n")
	writeFile(t, filepath.Join(second, "extra.md"), "---\nname: extra\ndescription: extra\n---\nC\n")

	skills, err := LoadSkillsDirs(first, second)
	if err != nil {
		t.Fatalf("LoadSkillsDirs: %v", err)
	}
	if got := skills.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2 (%v)", got, skills.Names())
	}
	sk, ok := skills.byName["shared"]
	if !ok || sk.Description != "first" {
		t.Fatalf("shared = %#v, want first-dir definition", sk)
	}
}

func TestLoadSkillsDirs_NoDirsIsError(t *testing.T) {
	if _, err := LoadSkillsDirs(); err == nil {
		t.Fatal("expected error for no dirs")
	}
}

func TestSkills_Tool(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "review", "SKILL.md"), "---\nname: code-review\n---\nCheck the diff.\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	tool := skills.Tool()
	if tool.Name != "read_skill" {
		t.Fatalf("tool name = %q, want read_skill", tool.Name)
	}
	out, err := tool.Execute(t.Context(), json.RawMessage(`{"name":"code-review"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "# Skill: code-review") || !strings.Contains(out, "Check the diff.") {
		t.Errorf("unexpected output: %q", out)
	}
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"name":"missing"}`)); err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestSkills_NilSafe(t *testing.T) {
	var s *Skills
	if s.Len() != 0 || s.Names() != nil || s.IndexBlock() != "" {
		t.Fatal("nil Skills should be empty")
	}
	if got := s.Tool(); got.Name != "" || got.Execute != nil {
		t.Fatal("nil Skills should return zero tool")
	}
}

func TestJoinSystemPrompt(t *testing.T) {
	cases := []struct {
		base, extra, want string
	}{
		{"", "x", "x"},
		{"x", "", "x"},
		{"a", "b", "a\n\nb"},
		{" a ", " b ", "a\n\nb"},
	}
	for _, tc := range cases {
		if got := joinSystemPrompt(tc.base, tc.extra); got != tc.want {
			t.Errorf("joinSystemPrompt(%q,%q) = %q, want %q", tc.base, tc.extra, got, tc.want)
		}
	}
}

func TestWithSystemPromptSuffix_Accumulates(t *testing.T) {
	o := New(WithSystemPromptSuffix("first"), WithSystemPromptSuffix("second"), WithSystemPromptSuffix("  "))
	if o.systemPromptSuffix != "first\n\nsecond" {
		t.Fatalf("suffix = %q, want first\\n\\nsecond", o.systemPromptSuffix)
	}
}

func TestWithSystemPromptSuffix_EmptyIsNoop(t *testing.T) {
	o := New(WithSystemPromptSuffix("   "))
	if o.systemPromptSuffix != "" {
		t.Fatalf("suffix = %q, want empty", o.systemPromptSuffix)
	}
}

func TestWithSkills_RegistersIndexAndTool(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.md"), "---\nname: s\ndescription: skill s\n---\nbody\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	o := New(WithSkills(skills))
	if !strings.Contains(o.systemPromptSuffix, "`s` — skill s") {
		t.Fatalf("systemPromptSuffix missing index: %q", o.systemPromptSuffix)
	}
	if !hasToolNamed(o.tools, "read_skill") {
		t.Fatal("read_skill tool not registered")
	}
}

func TestWithSkills_NilIsNoop(t *testing.T) {
	o := New(WithSkills(nil))
	if o.skills != nil || hasToolNamed(o.tools, "read_skill") {
		t.Fatal("WithSkills(nil) should not install anything")
	}
}

func TestWithSkills_ExplicitToolWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.md"), "---\nname: s\n---\nbody\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	// A caller-registered tool with the same name must win over the
	// built-in loader.
	custom := goai.Tool{Name: "read_skill", Description: "custom"}
	o := New(WithTools(custom), WithSkills(skills))
	count := 0
	for _, tool := range o.tools {
		if tool.Name == "read_skill" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("read_skill count = %d, want 1", count)
	}
}

func TestWithSkills_AppendsToCoordinatorPrompt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.md"), "---\nname: s\ndescription: skill s\n---\nbody\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	coord := NewDefaultCoordRunner(nil)
	New(WithCoordinator(coord), WithSkills(skills))
	if !strings.Contains(coord.systemPrompt, DefaultCoordSystemPrompt) {
		t.Fatal("coordinator base prompt lost")
	}
	if !strings.Contains(coord.systemPrompt, "`s` — skill s") {
		t.Fatalf("coordinator prompt missing skills index: %q", coord.systemPrompt)
	}
}

func TestApplyExecutorOptions_ThreadsSystemPromptSuffix(t *testing.T) {
	o := New(WithSystemPromptSuffix("operator policy"))
	exec := &Executor{}
	o.applyExecutorOptions(exec)
	if exec.SystemPromptSuffix != "operator policy" {
		t.Fatalf("SystemPromptSuffix = %q, want operator policy", exec.SystemPromptSuffix)
	}
}

func TestFallbackSkillName(t *testing.T) {
	if got := fallbackSkillName("/a/b/SKILL.md"); got != "b" {
		t.Errorf("SKILL.md fallback = %q, want b", got)
	}
	if got := fallbackSkillName("/a/b/notes.md"); got != "notes" {
		t.Errorf("notes.md fallback = %q, want notes", got)
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	if got := firstNonEmptyLine("\n\n## Header\nbody"); got != "Header" {
		t.Errorf("got %q, want Header", got)
	}
	long := strings.Repeat("x", 200)
	if got := firstNonEmptyLine(long); len(got) != 160 {
		t.Errorf("long line length = %d, want 160", len(got))
	}
	if got := firstNonEmptyLine("\n \n"); got != "" {
		t.Errorf("blank body = %q, want empty", got)
	}
}

// systemCapturingModel records the system prompt sent to the provider.
type systemCapturingModel struct {
	system string
	calls  int
}

func (m *systemCapturingModel) ModelID() string { return "capturing-system" }
func (m *systemCapturingModel) Capabilities() provider.ModelCapabilities {
	return provider.ModelCapabilities{}
}
func (m *systemCapturingModel) DoGenerate(_ context.Context, p provider.GenerateParams) (*provider.GenerateResult, error) {
	m.calls++
	m.system = p.System
	return &provider.GenerateResult{Text: "ok"}, nil
}
func (m *systemCapturingModel) DoStream(_ context.Context, _ provider.GenerateParams) (*provider.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestExecutorStep_InjectsSystemPromptSuffix proves the operator-level
// suffix reaches the per-step LLM system slot, appended after the
// workflow-declared agent prompt.
func TestExecutorStep_InjectsSystemPromptSuffix(t *testing.T) {
	model := &systemCapturingModel{}
	wf := &Workflow{
		Name:   "wf",
		Agents: map[string]AgentConfig{"a": {Description: "agent", Prompt: "BASE"}},
		Steps:  []Step{{ID: "s1", Agent: "a", Instructions: "do the thing"}},
	}
	exec := &Executor{
		Runner:             &AgentRunner{model: model},
		Storage:            NewMemoryStorage(),
		Workflow:           wf,
		SystemPromptSuffix: "EXTRA",
	}
	if _, err := exec.Run(context.Background()); err != nil {
		t.Fatalf("exec.Run: %v", err)
	}
	if model.calls == 0 {
		t.Fatal("model was never called")
	}
	if model.system != "BASE\n\nEXTRA" {
		t.Fatalf("system = %q, want BASE\\n\\nEXTRA", model.system)
	}
}

func TestLoadSkillFile_ErrorPaths(t *testing.T) {
	if _, err := loadSkillFile(filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := loadSkillFile(t.TempDir()); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("err = %v, want is a directory", err)
	}
}

func TestLoadSkills_SkipsNonMarkdownAndEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.txt"), "not a skill")
	writeFile(t, filepath.Join(dir, "empty", "notes.txt"), "nothing")
	writeFile(t, filepath.Join(dir, "ok.md"), "body")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if got := skills.Names(); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("Names = %v, want [ok]", got)
	}
}

func TestLoadSkillFile_ReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.md")
	writeFile(t, path, "body")
	orig := readSkillFile
	t.Cleanup(func() { readSkillFile = orig })
	readSkillFile = func(string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := loadSkillFile(path); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestLoadSkillFile_OversizeTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.md")
	writeFile(t, path, "x")
	orig := readSkillFile
	t.Cleanup(func() { readSkillFile = orig })
	readSkillFile = func(string) ([]byte, error) {
		return []byte(strings.Repeat("a", skillMaxBytes+100)), nil
	}
	sk, err := loadSkillFile(path)
	if err != nil {
		t.Fatalf("loadSkillFile: %v", err)
	}
	if len(sk.Body) != skillMaxBytes {
		t.Fatalf("body len = %d, want %d", len(sk.Body), skillMaxBytes)
	}
}

func TestLoadSkills_BrokenSymlinkIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "broken.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := LoadSkills(dir); err == nil {
		t.Fatal("expected error for broken symlink skill")
	}
}

func TestSplitFrontmatter_InvalidYAML(t *testing.T) {
	name, desc, body := splitFrontmatter("---\n: bad: [\n---\nbody\n")
	if name != "" || desc != "" || !strings.Contains(body, "body") {
		t.Fatalf("malformed frontmatter should fall through: name=%q desc=%q body=%q", name, desc, body)
	}
}

func TestFirstNonEmptyLine_HeadingOnlyLine(t *testing.T) {
	if got := firstNonEmptyLine("###\ntext"); got != "text" {
		t.Errorf("got %q, want text", got)
	}
}

func TestLoadSkills_UnreadableFileIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.md")
	writeFile(t, path, "body")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if f, err := os.Open(path); err == nil {
		_ = f.Close()
		t.Skip("cannot create unreadable file in this environment")
	}
	if _, err := LoadSkills(dir); err == nil {
		t.Fatal("expected error for unreadable skill file")
	}
}

func TestLoadSkillsDirs_PropagatesError(t *testing.T) {
	good := t.TempDir()
	writeFile(t, filepath.Join(good, "a.md"), "body")
	if _, err := LoadSkillsDirs(good, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error from missing dir")
	}
}

func TestRunAgent_InjectsSystemPromptSuffix(t *testing.T) {
	model := &systemCapturingModel{}
	orch := New(WithModel(model), WithSystemPromptSuffix("OPERATOR_POLICY"))
	if _, err := orch.RunAgent(context.Background(), AgentConfig{Prompt: "hello"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if model.system != "OPERATOR_POLICY" {
		t.Fatalf("system = %q, want OPERATOR_POLICY", model.system)
	}
}
