---
title: YAML reference
description: 'zenflow workflows are YAML (or JSON) documents that conform to the v1 schema. This page is the entry point: it lists every top-level field, points...'
---

# YAML reference

zenflow workflows are YAML (or JSON) documents that conform to the v1 schema. This page is the entry point: it lists every top-level field, points at the authoritative spec sources, and shows how to validate a workflow before running it.

The schema is versioned and lives in the zenflow repo at [`spec/v1/`](https://github.com/zendev-sh/zenflow/tree/main/spec/v1). Two artifacts are normative:

- [`spec/v1/spec.md`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/spec.md) - prose specification with examples and edge cases.
- [`spec/v1/schema.json`](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/schema.json) - JSON Schema (Draft 2020-12) source of truth.

If the prose and the schema disagree, the schema wins.

## Top-level fields

A workflow document has these top-level fields:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `name` | string | yes | Workflow name. `minLength: 1`. |
| `description` | string | no | Free-form. Documents intent for humans. |
| `version` | integer | no | Schema version. Defaults to `1`. Must be `>= 1`. v1 validators reject unknown values. |
| `agents` | map[string, AgentConfig] | no | Named agent definitions. Keys are referenced by `step.agent`. |
| `includes` | map[string, string] | no | Named sub-workflow registry. Values are file paths relative to the workflow file. |
| `steps` | array[Step] | yes | DAG nodes. `minItems: 1`. |
| `options` | Options | no | Execution configuration. |

Unknown top-level fields are rejected (`additionalProperties: false`).

## Minimal example

The smallest valid workflow has a name and one step:

```yaml
name: minimal
steps:
  - id: greet
    instructions: "Say hello."
```

Same document in JSON:

```json
{
  "name": "minimal",
  "steps": [
    { "id": "greet", "instructions": "Say hello." }
  ]
}
```

Both parse identically. YAML is the recommended format for human-authored workflows; JSON is what an LLM coordinator emits when `zenflow goal` decomposes a goal into a workflow.

## Page map

| Topic | Page |
| --- | --- |
| Workflow-level fields | [`/yaml/workflow`](./workflow) |
| Agent definitions | [`/yaml/agent`](./agent) |
| Step fields | [`/yaml/step`](./step) |
| Loops (`forEach`, `repeat-until`) | [`/yaml/loop`](./loop) |
| CEL expressions in `condition` and `forEach` | [`/yaml/cel-reference`](./cel-reference) |

For runtime semantics (DAG scheduling, failure strategies, retries), see the [Concepts](/concepts/) section.

## Stability promise

The `spec/v1/` directory is stable. Within a major version, changes are additive only:

- New optional fields may be added to existing objects.
- New enum values may be added to enums (`onStepFailure`, `scheduler`, `loop.outputMode`).
- Existing required fields will not be removed or renamed.
- Existing field semantics will not change.

Breaking changes go to a new major version (`spec/v2/`). Old schemas remain available at their version paths. Pin your tooling to the version you author for if you want strict compatibility:

```yaml
name: my-workflow
version: 1
steps:
  - id: ...
```

## Validating a workflow

### With the zenflow CLI

`zenflow validate` runs the same loader the engine uses. It checks JSON Schema, then runs the validator-only rules (cycle detection, cross-namespace `dependsOn`, mutual exclusion of `include` fields, `untilAgent.resultSchema` shape).

```bash
zenflow validate workflow.yaml
# ✓ Valid
```

Exit code is `0` on success, `2` on validation error, `3` on usage error. See [CLI / Commands](../cli/commands) for the full table.

### With ajv-cli

For pre-commit hooks or CI lanes that should not depend on a Go binary, validate against the JSON Schema directly. ajv supports YAML via the `js-yaml` reader:

```bash
npm install -g ajv-cli ajv-formats js-yaml

# Convert YAML to JSON, then validate.
node -e "const y=require('js-yaml');const fs=require('fs');\
  console.log(JSON.stringify(y.load(fs.readFileSync('workflow.yaml','utf8'))))" \
  | ajv validate \
      -s https://raw.githubusercontent.com/zendev-sh/zenflow/main/spec/v1/schema.json \
      --spec=draft2020 \
      -d -
```

Or vendor the schema and validate directly:

```bash
curl -sLO https://raw.githubusercontent.com/zendev-sh/zenflow/main/spec/v1/schema.json
ajv validate -s schema.json --spec=draft2020 -d workflow.json
```

JSON Schema only enforces structural rules. The validator rules (DAG cycles, referential integrity for `agent`/`dependsOn`/`untilAgent`, `loop` mutual exclusion) require the zenflow CLI or library.

## Authoring conventions

- **YAML for humans, JSON for machines.** Both serializations parse identically.
- **No YAML anchors, tags, or merge keys.** The spec uses a portable subset.
- **File references via `@`.** A string starting with `@` in `agents.*.prompt` or `steps.*.instructions` is read from disk relative to the workflow file. `contextFiles` does not use the prefix; its values are always paths.
- **Keep step IDs greppable.** They appear in logs, JSON events, and error messages. The pattern is `^[a-zA-Z][a-zA-Z0-9_-]*$`.
- **Co-locate prompts.** Put long-form prompts in `prompts/` next to the workflow file and reference them with `@prompts/...`. The 19 reference workflows in [`spec/v1/examples/`](https://github.com/zendev-sh/zenflow/tree/main/spec/v1/examples) follow this convention.
