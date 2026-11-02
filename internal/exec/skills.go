package exec

// skills.go - progressive-disclosure skill loading. A skills directory
// holds one skill per subdirectory (``<dir>/<slug>/SKILL.md``) or one
// flat markdown file per skill (``<dir>/<slug>.md``). Each skill carries
// optional YAML frontmatter (``name`` / ``description``) followed by a
// markdown body. LoadSkills builds a compact index for the system prompt
// and a ``read_skill`` tool the agent can call to pull the full body on
// demand, keeping context usage proportional to what the agent actually
// uses instead of the size of the whole skills tree.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zendev-sh/goai"
	"gopkg.in/yaml.v3"
)

// skillFileName is the markdown file name recognised inside a skill
// subdirectory.
const skillFileName = "SKILL.md"

// skillMaxBytes caps how much of a skill body is read. A skill is
// instructions, not data; 1 MiB is far above any realistic SKILL.md and
// protects against an accidental binary or huge file.
const skillMaxBytes = 1 << 20

// Skill is one loaded skill.
type Skill struct {
	// Name is the skill identifier used by the read_skill tool. It comes
	// from frontmatter when present, otherwise from the directory or
	// file stem.
	Name string
	// Description is a one-line summary shown in the system-prompt
	// index. Comes from frontmatter when present, otherwise from the
	// first non-empty body line.
	Description string
	// Path is the absolute path to the skill's markdown file.
	Path string
	// Body is the full markdown body with frontmatter stripped.
	Body string
}

// Skills is an immutable collection of loaded skills plus their
// pre-rendered system-prompt index.
type Skills struct {
	skills []Skill
	byName map[string]Skill // lower-cased name -> skill
	index  string
}

// LoadSkills loads every skill under dir. Supported layouts:
//
//	<dir>/<slug>/SKILL.md
//	<dir>/<slug>.md
//
// Subdirectories without a SKILL.md are skipped. Files that are not
// markdown are skipped. An unreadable dir, or a dir containing no
// skills, is an error so a mistyped path surfaces at startup instead of
// silently running without instructions.
func LoadSkills(dir string) (*Skills, error) {
	// Prefer the absolute path so Skill.Path is stable regardless of the
	// caller's later chdir; fall back to the cleaned input when Abs fails
	// (e.g. the process cwd was removed) so the load itself still works.
	absDir := filepath.Clean(dir)
	if abs, absErr := filepath.Abs(dir); absErr == nil {
		absDir = abs
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("skills: read dir %q: %w", dir, err)
	}
	loaded := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(absDir, entry.Name())
		var skillPath string
		switch {
		case entry.IsDir():
			candidate := filepath.Join(path, skillFileName)
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				skillPath = candidate
			}
		case strings.EqualFold(filepath.Ext(entry.Name()), ".md"):
			skillPath = path
		}
		if skillPath == "" {
			continue
		}
		skill, loadErr := loadSkillFile(skillPath)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = append(loaded, skill)
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("skills: no skills found in %q (expected <dir>/*/SKILL.md or <dir>/*.md)", dir)
	}
	return newSkills(loaded), nil
}

// LoadSkillsDirs loads multiple skills directories and merges them.
// When two directories declare the same skill name the first directory
// wins, so callers can layer a project dir over a shared dir by putting
// the project dir first. Each directory must independently contain at
// least one skill.
func LoadSkillsDirs(dirs ...string) (*Skills, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("skills: no directories supplied")
	}
	merged := make([]Skill, 0)
	seen := make(map[string]struct{})
	for _, dir := range dirs {
		loaded, err := LoadSkills(dir)
		if err != nil {
			return nil, err
		}
		for _, sk := range loaded.skills {
			key := strings.ToLower(sk.Name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, sk)
		}
	}
	return newSkills(merged), nil
}

// newSkills builds the lookup map and the deterministic index block.
func newSkills(skills []Skill) *Skills {
	sort.SliceStable(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	byName := make(map[string]Skill, len(skills))
	var sb strings.Builder
	sb.WriteString("## Available Skills\n")
	sb.WriteString("Skill instructions are not loaded by default. When a task matches one of the\n")
	sb.WriteString("descriptions below, call the `read_skill` tool with the skill name to load its\n")
	sb.WriteString("full instructions, then follow them.\n\n")
	for _, sk := range skills {
		byName[strings.ToLower(sk.Name)] = sk
		sb.WriteString("- `")
		sb.WriteString(sk.Name)
		sb.WriteString("`")
		if sk.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(sk.Description)
		}
		sb.WriteString("\n")
	}
	return &Skills{skills: skills, byName: byName, index: sb.String()}
}

// loadSkillFile reads and parses one skill markdown file.
func loadSkillFile(path string) (Skill, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: stat %q: %w", path, err)
	}
	if info.IsDir() {
		return Skill{}, fmt.Errorf("skills: %q is a directory, expected a markdown file", path)
	}
	data, err := readSkillFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: read %q: %w", path, err)
	}
	if len(data) > skillMaxBytes {
		data = data[:skillMaxBytes]
	}
	name, description, body := splitFrontmatter(string(data))
	if name == "" {
		name = fallbackSkillName(path)
	}
	if description == "" {
		description = firstNonEmptyLine(body)
	}
	return Skill{
		Name:        name,
		Description: description,
		Path:        path,
		Body:        body,
	}, nil
}

// readSkillFile reads a skill file. Injectable so tests can exercise the
// read-error path without relying on filesystem permissions (which root
// bypasses in CI).
var readSkillFile = os.ReadFile

// splitFrontmatter extracts optional YAML frontmatter delimited by a
// leading “---“ line and the next “---“ line. Returns the parsed
// name/description plus the body with the frontmatter removed. Malformed
// frontmatter is ignored (the whole file is treated as the body) rather
// than failing the load - a skill with a YAML typo should still be
// readable.
func splitFrontmatter(content string) (name, description, body string) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", content
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &meta); err != nil {
		return "", "", content
	}
	body = strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\r\n")
	return strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description), body
}

// fallbackSkillName derives a display name from the file path: the
// containing directory for “<slug>/SKILL.md“, or the file stem for a
// flat “<slug>.md“.
func fallbackSkillName(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.EqualFold(stem, "skill") || strings.EqualFold(stem, "SKILL") {
		if dir := filepath.Base(filepath.Dir(path)); dir != "" && dir != "." && dir != string(filepath.Separator) {
			return dir
		}
	}
	return stem
}

// firstNonEmptyLine returns the first non-blank body line, stripped of
// leading markdown heading markers, capped to a readable length.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 160 {
			line = line[:160]
		}
		return line
	}
	return ""
}

// Names returns the loaded skill names in sorted order.
func (s *Skills) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, len(s.skills))
	for i, sk := range s.skills {
		names[i] = sk.Name
	}
	return names
}

// Len returns the number of loaded skills.
func (s *Skills) Len() int {
	if s == nil {
		return 0
	}
	return len(s.skills)
}

// IndexBlock returns the markdown block to append to an agent's system
// prompt, or "" when no skills are loaded.
func (s *Skills) IndexBlock() string {
	if s == nil || len(s.skills) == 0 {
		return ""
	}
	return s.index
}

// Tool returns the “read_skill“ goai.Tool bound to this collection.
// It returns nil when no skills are loaded so callers can skip
// registration.
func (s *Skills) Tool() goai.Tool {
	if s == nil || len(s.skills) == 0 {
		return goai.Tool{}
	}
	return goai.NewTool("read_skill",
		"Load the full instructions for a skill by name. Call this before following a skill listed in the Available Skills section.",
		func(_ context.Context, in struct {
			Name string `json:"name" jsonschema:"description=The skill name exactly as listed in the Available Skills section"`
		}) (string, error) {
			key := strings.ToLower(strings.TrimSpace(in.Name))
			sk, ok := s.byName[key]
			if !ok {
				return "", fmt.Errorf("unknown skill %q (available: %s)", in.Name, strings.Join(s.Names(), ", "))
			}
			return fmt.Sprintf("# Skill: %s\n\n%s", sk.Name, sk.Body), nil
		})
}
