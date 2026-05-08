---
title: Composition
description: Workflows can include other workflows. The includes registry maps names to file paths; a step references a name (or a path directly) via include....
---

# Composition

Workflows can include other workflows. The `includes` registry maps names to file paths; a step references a name (or a path directly) via `include`. The included sub-workflow's steps execute as if they were part of the parent, with namespaced step IDs to avoid collision.

This is how you reuse a deploy flow across staging and production, share an audit checklist across multiple parent flows, or split a big workflow into reviewable pieces.

## Two-level design

**Top-level `includes`** maps friendly names to file paths:

```yaml
includes:
  deploy: "workflows/deploy.yaml"
  auth-flow: "workflows/auth-flow.yaml"
```

**Step-level `include`** references a name from the registry, or a file path directly:

```yaml
steps:
  - id: deploy-staging
    include: deploy           # named reference
    dependsOn: [build]

  - id: setup-auth
    include: "workflows/auth-flow.yaml"  # direct path (no registry entry needed)
    dependsOn: [design]
```

You do not need an `includes` registry to use direct-path references. The registry is sugar for repeated includes - if the same sub-workflow is referenced in many places, naming it once keeps things tidy.

## Worked example: include-reuse

```yaml
name: include-reuse
description: Build, then deploy to staging and production using a reusable deploy workflow.

includes:
  deploy: "workflows/deploy.yaml"

agents:
  architect:
    description: "System architect who designs the deployment strategy."
    model: "claude-opus-4-6"
  builder:
    description: "Build engineer who compiles and packages the application."
    model: "claude-sonnet-4-6"
    tools: [bash]

steps:
  - id: design
    agent: architect
    instructions: "Design the deployment strategy for the new release."

  - id: build
    agent: builder
    instructions: "Build and package the application. Run all tests."
    dependsOn: [design]
    timeout: "20m"

  - id: deploy-staging
    include: deploy
    dependsOn: [build]
    timeout: "30m"
    retries: 1

  - id: deploy-production
    include: deploy
    dependsOn: [deploy-staging]
    timeout: "45m"
    retries: 2

options:
  onStepFailure: abort
```

What happens:

1. `design` runs.
2. `build` runs.
3. `deploy-staging` is an include step. The executor parses `workflows/deploy.yaml`, runs every step inside it under the namespace `deploy-staging.<inner-step-id>`. The whole sub-workflow is bounded by the outer step's `timeout: "30m"` and `retries: 1`.
4. `deploy-production` does the same with the same sub-workflow file but a different namespace prefix and different bounds. Same `deploy.yaml`, two independent runs.

The sub-workflow author writes one deploy flow and parent workflows reuse it. The parent author wires it into different positions in the outer DAG.

## Step-level include rules

A step with `include` is a delegation step. It cannot do anything else. The validator rejects an include step that also has:

- `agent`
- `instructions`
- `loop`
- `condition`
- `contextFiles`
- `model`

These would be ambiguous - should `condition` apply before or after the sub-workflow loads? The clean answer is "use a wrapper step". To gate an included flow on a condition, put a separate guard step with a condition before it:

```yaml
steps:
  - id: precheck
    agent: validator
    instructions: "Decide whether to deploy."
    # ... emits result.should_deploy

  - id: deploy
    include: deploy
    dependsOn: [precheck]
    # condition not allowed here
```

Move the gate up:

```yaml
  - id: deploy-gate
    agent: gatekeeper
    dependsOn: [precheck]
    condition: "steps.precheck.result.should_deploy == true"
    instructions: "Approve or block."

  - id: deploy
    include: deploy
    dependsOn: [deploy-gate]
```

A step with `include` may have these fields:

- `dependsOn` - applies to the sub-workflow as a whole.
- `timeout` - applies to the entire sub-workflow execution.
- `retries` - retries the sub-workflow from the beginning.
- `id` - the parent step ID (used as namespace prefix).

## Sub-workflow loading

The sub-workflow file is parsed as a full zenflow document. It can have its own `agents`, `includes`, `options`. Specific behaviours:

- **Agent merge.** Sub-workflow agents merge into the parent scope. Name collisions are an error at load time.
- **Step ID namespacing.** Sub-workflow step IDs are prefixed: `{parent-step-id}.{inner-step-id}`. The deploy sub-workflow's step `run-tests` becomes `deploy-staging.run-tests` in the staging include and `deploy-production.run-tests` in the production include.
- **Recursive includes.** A sub-workflow can itself contain includes. Includes are hard-capped at depth 5 (`MaxIncludeDepth`). Going deeper returns a `ValidationError`. Sub-workflow expansion (which counts nested loops + includes after expansion) has a separate cap of 20 (`MaxNestingDepth`).
- **Path resolution.** File paths in `includes` are relative to the **including** workflow file's directory, not the working directory.

## Reference resolution

When a step has `include: foo`:

1. If `foo` matches a key in the parent's top-level `includes` map, the value (a file path) is the target.
2. Otherwise, `foo` is treated as a file path directly.

Mix named and direct references in the same workflow.

## Cross-namespace `dependsOn`

Inner step IDs are scoped to the sub-workflow. An outer step cannot reference inner step IDs, and inner steps cannot reference outer step IDs. Cross-scope qualified references are rejected by the step-ID validator, which only accepts `[a-zA-Z][a-zA-Z0-9_-]*` (dots are not allowed in step IDs), so any qualified form like `deploy.verify` fails to parse:

```yaml
# Top-level workflow
steps:
  - id: build
    # ...

  - id: deploy
    include: deploy

  - id: smoke-test
    dependsOn: [deploy.verify]   # ERROR: "deploy.verify" is not a valid step ID
```

To pass data from a sub-workflow to a downstream outer step, depend on the include step itself (`dependsOn: [deploy]`). The include step's content / result aggregates the sub-workflow's terminal state. Use sub-workflow result aggregation instead of trying to address inner steps directly.

## When to compose

Compose when:

- **The same flow runs multiple times.** Deploy to staging then production. Audit code, then audit configs, with the same audit logic.
- **The workflow is too big to read on one screen.** A 30-step workflow split into 5 sub-workflows of 6 steps each is easier to reason about and review.
- **Different teams own different pieces.** The platform team owns `deploy.yaml`, the application team owns the parent flow that calls it.

Do not compose when:

- The "sub-workflow" is one step. Just write the step inline.
- The composition crosses concerns: an include is a black box from the parent's point of view. If the parent needs to inspect / branch on intermediate sub-workflow state, inline the steps instead.

## Composition vs forEach

Both produce many invocations of similar work, but they are different shapes:

- **Composition** (include): one definition reused at distinct, named positions in a DAG. Each include has its own `dependsOn`, `timeout`, `retries`. The parent author chooses where each invocation goes.
- **forEach**: one definition iterated over a runtime array. Each iteration is anonymous (indexed), runs in parallel, sees one element of the input.

If you have N "deploy" calls with N different positions, names, timeouts, and dependencies, use composition. If you have N parallel deploy calls all behaving identically except for the input data, use forEach.

## Cross-links

- [DAG scheduling](/concepts/dag-scheduling) - the parent DAG and its include nodes
- [Loops](/concepts/loops) - the forEach alternative for parallel-uniform work
- [Conditions](/concepts/conditions) - how to gate an include with a wrapper step
- [YAML: Workflow](/yaml/workflow) - the `includes` field in the schema
