---
title: Skills
description: 'Progressive-disclosure instruction bundles: a compact index in the system prompt plus a read_skill tool that loads the full body on demand.'
---

# Skills

A **skill** is a reusable instruction bundle - a `SKILL.md` file (or a flat `.md` file) that teaches an agent a procedure: how to review a diff, run a release, follow a house style. Skills are mounted once and shared by every agent in a run, so project knowledge does not have to be copied into each `agents.<name>.prompt`.

Loading is **progressive**: the system prompt gets only a compact index (skill name + description). The full body is returned by the `read_skill` tool when the agent decides it needs it. Context usage therefore tracks the skills actually used, not the size of the skills tree.

## Directory layout

Both layouts work, and can be mixed in one directory:

```text
skills/
├── code-review/
│   └── SKILL.md          # directory form
├── release-checklist.md  # flat form
└── notes/                # no SKILL.md -> skipped
```

`LoadSkills` scans the top level of the directory:

- A **subdirectory** is a skill when it contains a `SKILL.md`; the rest of that subdirectory is ignored by the loader (the body may still reference files next to it).
- A **flat `.md` file** at the top level is a skill.
- Other files and subdirectories without a `SKILL.md` are skipped.

An unreadable directory, or a directory containing no skills, is an error - the CLI exits `3` before the first LLM call.

## Frontmatter

Frontmatter is optional. When present it supplies the index entry:

```markdown
---
name: code-review
description: Review a diff for correctness, security, and missing tests.
---
# Code Review

Read the diff, then report findings as `file:line - severity - issue`.
```

- `name` fails back to the directory name (for `<slug>/SKILL.md`) or the file stem (for `<slug>.md`).
- `description` falls back to the first non-empty body line (heading markers stripped).
- Malformed frontmatter is ignored: the whole file is treated as the body, and the fallback name/description apply.

Names are matched case-insensitively by `read_skill`. When several directories are layered, the **first** directory wins on a duplicate name, so put the project-specific directory before a shared one.

## CLI

`--skills DIR` is accepted by `flow`, `goal`, and `agent`. Repeat it to layer directories:

```bash
zenflow flow review.yaml \
  --model fptcloud/DeepSeek-V4.1-Flash \
  --system-prompt @./review-policy.md \
  --skills ./skills \
  --skills ../shared-skills
```

The flag is CLI-only: there is no `skills:` field in workflow YAML. The same run from a library embedding uses [`WithSkills`](#go-api).

Because `read_skill` is an ordinary tool, the permission gate applies. On a non-interactive terminal, allow it explicitly:

```bash
zenflow agent "review the diff" --skills ./skills --strict --allow read_skill
```

`--sandbox` can also allow it by adding it on top of the safe set: `--sandbox --allow read_skill`. Without an allow entry, strict mode (and therefore sandbox mode) denies the tool, and the agent cannot load any skill body.

## Go API

```go
skills, err := zenflow.LoadSkills("./skills")
if err != nil {
    return err
}

orch := zenflow.New(
    zenflow.WithModel(model),
    zenflow.WithSkills(skills),
    zenflow.WithSystemPromptSuffix("Always cite file:line."),
)
defer orch.Close()
```

- `LoadSkills(dir)` loads one directory; `LoadSkillsDirs(dirs...)` merges several (first wins).
- `WithSkills(s)` installs the collection on the orchestrator: `New` registers the `read_skill` tool on the catalog and appends `s.IndexBlock()` to the shared system-prompt suffix.
- `Skills.Names()`, `Skills.Len()`, `Skills.IndexBlock()`, and `Skills.Tool()` expose the collection for callers that want to build their own prompt or tool wiring.

## Prompt ordering

For every step agent the system slot is built as:

1. `agents.<name>.prompt` - the workflow-declared role (the base).
2. `--system-prompt` / `WithSystemPromptSuffix` - operator policy, appended after the base.
3. The skills index - appended after the operator policy.

The coordinator installed via `WithCoordinator` receives the same operator policy and skills index, so coordinator behaviour follows the same rules as the step agents. Standalone `RunAgent` calls receive them too; with no workflow-declared agent prompt, the operator policy becomes the system slot directly.

## Interaction with tool filtering

The `read_skill` tool is added to the orchestrator catalog, not to every agent unconditionally. An agent that declares a `tools:` allowlist only sees the tools it lists - include `read_skill` there:

```yaml
agents:
  reviewer:
    description: Reviews the change.
    tools: ["read", "grep", "read_skill"]
```

An agent with no `tools:` field gets every registered tool, including `read_skill`. A caller that registers its own tool named `read_skill` keeps it: the built-in loader does not overwrite an existing entry.

Tool steps (`tool: read` with `toolInput`) do not run an LLM, so they never use skills.

## See also

- [Agents](/concepts/agents) - where the base system prompt comes from.
- [Tools](/concepts/tools) - the catalog and the permission gate.
- [Go API: options](/api/options) - `WithSystemPromptSuffix`, `WithSkills`, and the full option set.
- [CLI flags](/cli/flags#agent-context-and-skills) - `--system-prompt` and `--skills`.
