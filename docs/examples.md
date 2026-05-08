---
title: Examples
description: zenflow ships 19 reference workflows under spec/v1/examples/ and matching Go embeddings under examples/. This page walks through them grouped by...
---

# Examples

zenflow ships 19 reference workflows under [`spec/v1/examples/`](https://github.com/zendev-sh/zenflow/tree/main/spec/v1/examples) with matching Go embeddings under [`examples/`](https://github.com/zendev-sh/zenflow/tree/main/examples) (18 of 19; retries-and-sampling is YAML only). This page walks through them grouped by what they demonstrate.

Every example has the same shape:

- A YAML file at `spec/v1/examples/<name>.yaml` you can run from the CLI.
- A Go embedding at `examples/<name>/main.go` you can run with `go run`.

To run an example from the CLI, set an API key + default model, then point `zenflow flow` at the YAML:

```bash
export GEMINI_API_KEY=...
export ZENFLOW_MODEL=google/gemini-2.0-flash    # or another PROVIDER/MODEL your key supports
zenflow flow spec/v1/examples/<name>.yaml
```

Each `Run:` line below shows the YAML path only; the same `GEMINI_API_KEY` + `ZENFLOW_MODEL` pair (or a per-command `--model` flag) applies to every example.

To run the Go embedding:

```bash
go run ./examples/<name>
```

## Single agent

### minimal

The smallest valid workflow. One step, no `agents` section.

```yaml
# spec/v1/examples/minimal.yaml
name: minimal
steps:
  - id: greet
    instructions: "Say hello."
```

What it demonstrates: a workflow does not require an `agents:` map. Bare steps run with the orchestrator's default agent and default model.

Run: `zenflow flow spec/v1/examples/minimal.yaml`. Embed: [`examples/minimal/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/minimal/main.go).

## Sequential

### simple-chain

Linear three-step pipeline. Each step waits for the previous one.

```yaml
# spec/v1/examples/simple-chain.yaml
name: simple-chain
description: Generate, review, and publish a blog post.

steps:
  - id: draft
    instructions: "Write a 500-word blog post about async programming in Go."

  - id: review
    instructions: "Review the draft for technical accuracy and readability. Suggest improvements."
    dependsOn: [draft]

  - id: publish
    instructions: "Format the reviewed post as markdown and add frontmatter metadata."
    dependsOn: [review]
```

What it demonstrates: `dependsOn` is the only sequencing primitive. No `agents:` section is needed when every step uses the default agent.

Run: `zenflow flow spec/v1/examples/simple-chain.yaml`. Embed: [`examples/simple-chain/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/simple-chain/main.go).

## Parallel

### parallel-fan-out

Three steps share a single dependency and run in parallel; a fourth step waits for all three.

```yaml
# spec/v1/examples/parallel-fan-out.yaml
name: parallel-fan-out
description: Design a microservice, implement components in parallel, then integrate.

agents:
  architect: { description: "System architect who designs APIs and data models." }
  backend:   { description: "Backend developer who implements Go services." }
  frontend:  { description: "Frontend developer who builds React components." }
  integrator: { description: "Integration engineer who wires components together." }

steps:
  - id: design
    agent: architect
    instructions: "Design the REST API schema and database models for a user auth service."
```

What it demonstrates: parallel fan-out is implied by graph shape - no keyword. `MaxConcurrency` (default 5) caps in-flight steps; raise via `WithMaxConcurrency`.

Run: `zenflow flow spec/v1/examples/parallel-fan-out.yaml`. Embed: [`examples/parallel-fan-out/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/parallel-fan-out/main.go).

## Coordinator-mediated

These examples show the coordinator forwarding messages between running peers via hub-and-spoke. Each peer agent sends with `send_message`; the coordinator decides where the message lands.

### debate

Two debate teams argue in parallel; the coordinator forwards each side's argument into the other's mailbox; a judge waits for both.

```yaml
# spec/v1/examples/debate.yaml
name: debate
description: Structured debate between two teams with coordinator-mediated argument exchange.

agents:
  moderator-setup: { description: "Debate moderator who defines the topic, rules, and key questions." }
  pro:             { description: "Debate team arguing IN FAVOR of the proposition." }
  con:             { description: "Debate team arguing AGAINST the proposition." }
  judge:           { description: "Impartial judge who declares a winner with reasoning." }

steps:
  - id: setup
    agent: moderator-setup
    instructions: "Define the debate topic, 3 key questions each side must address, and the judging criteria."

  - id: team-pro
    agent: pro
    instructions: "Argue IN FAVOR. Address the key questions from setup."
    dependsOn: [setup]

  # ... team-con and verdict steps elided
```

What it demonstrates: parallel peers exchanging context through the coordinator. The `setup` step runs first to fix the topic and rubric; `team-pro` and `team-con` then run in parallel and would never see each other's arguments without the coordinator.

Run: `zenflow flow spec/v1/examples/debate.yaml`. Embed: [`examples/debate/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/debate/main.go).

### debate-soak

A reduced two-step variant of `debate` (argue then judge) used to soak-test tail-latency-sensitive providers. Same coordinator/router code paths, no fan-in context bloat.

```yaml
# spec/v1/examples/debate-soak.yaml
name: debate-soak
description: Reduced 2-step debate (argue then judge) for soak coverage.

agents:
  pro:   { description: "Debate team arguing IN FAVOR." }
  judge: { description: "Impartial judge." }
```

What it demonstrates: how to scope down a workflow when one provider's tail latency is the bottleneck.

Run: `zenflow flow spec/v1/examples/debate-soak.yaml`. Embed: [`examples/debate-soak/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/debate-soak/main.go).

### research-team

A researcher completes first, then writer, fact-checker, and illustrator work in parallel. The coordinator forwards research findings to all three and cross-pollinates discoveries between them as they complete.

```yaml
# spec/v1/examples/research-team.yaml
name: research-team
description: Research a topic, then have writer, fact-checker, and illustrator collaborate via coordinator messaging.

agents:
  researcher:   { description: "Research analyst." }
  writer:       { description: "Technical writer." }
  fact-checker: { description: "Fact-checker." }
  illustrator:  { description: "Diagram designer." }
```

What it demonstrates: one-to-many forwarding. The coordinator can route the same research findings to three different running peers without each peer re-asking.

Run: `zenflow flow spec/v1/examples/research-team.yaml`. Embed: [`examples/research-team/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/research-team/main.go).

### product-launch

Market research drives parallel pricing, marketing, and legal teams. The coordinator forwards pricing decisions to the marketing copywriter and legal constraints to both pricing and marketing.

```yaml
# spec/v1/examples/product-launch.yaml
name: product-launch
description: Plan a product launch with parallel pricing, marketing, and legal review.

agents:
  analyst:    { description: "Market analyst." }
  pricing:    { description: "Pricing strategist." }
  copywriter: { description: "Marketing copywriter." }
  legal:      { description: "Legal reviewer." }
  planner:    { description: "Launch coordinator." }
```

What it demonstrates: a realistic coordination scenario where three parallel branches all benefit from each other's mid-flight decisions, without any peer-to-peer dependency edges in the YAML.

Run: `zenflow flow spec/v1/examples/product-launch.yaml`. Embed: [`examples/product-launch/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/product-launch/main.go).

### code-review

Implementation completes first, then security and performance reviewers work in parallel with cross-review messaging via the coordinator. A lead synthesises both reviews.

```yaml
# spec/v1/examples/code-review.yaml
name: code-review
description: Implement a feature, then run parallel security and performance reviews with cross-review messaging.

agents:
  developer:   { description: "Senior developer." }
  security:    { description: "Security engineer." }
  performance: { description: "Performance engineer." }
  lead:        { description: "Tech lead synthesising findings." }
```

What it demonstrates: bidirectional forwarding between two parallel reviewers. Each review enriches the other while both are in flight.

Run: `zenflow flow spec/v1/examples/code-review.yaml`. Embed: [`examples/code-review/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/code-review/main.go).

### messaging-demo

(workflow `name:` field is `messaging-rounds`)

Three rounds of question-and-answer between an `asker` and an `expert`, where the two have never met. Every message goes through the coordinator hub.

```yaml
# spec/v1/examples/messaging-demo.yaml
name: messaging-rounds
description: 3-round Q/A between asker and expert via coordinator hub. Coord forwards.

agents:
  asker:      { description: "Curious user. Sends questions via send_message." }
  expert:     { description: "Knowledgeable expert. Reads inbox, answers." }
  summarizer: { description: "Reads conversation history and summarizes." }

steps:
  - id: asker-1
    agent: asker
    instructions: |
      Round 1. Send your FIRST question to the coordinator.
      Call `send_message` ONCE with text: "QUESTION_1: What is the capital of France?"
```

What it demonstrates: a pure messaging workflow with no dependsOn data flow - all coordination is in the message stream, mediated by the coordinator. This is the canonical demo for understanding hub-and-spoke.

Run: `zenflow flow spec/v1/examples/messaging-demo.yaml`. Embed: [`examples/messaging-demo/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/messaging-demo/main.go).

## Loops

### loop-foreach

Discover an array of services from a step's structured output, then deploy each one in parallel.

```yaml
# spec/v1/examples/loop-foreach.yaml
name: loop-foreach
description: Discover services and deploy each one in parallel.

steps:
  - id: list_services
    agent: discovery
    instructions: "List all microservices that need deployment."

  - id: deploy_each
    dependsOn: [list_services]
    loop:
      forEach: "steps.list_services.result.services"
      maxConcurrency: 3
      steps:
        - id: deploy
          agent: deployer
          instructions: "Deploy this service to its target region."
        # ... verify sub-step elided

  # ... summary step elided
```

What it demonstrates: dynamic parallelism. The number of iterations is unknown until `list_services` returns. Inner-DAG step IDs get namespaced (`deploy_each[0].deploy`, `deploy_each[1].deploy`, ...) so the coordinator can address them.

Run: `zenflow flow spec/v1/examples/loop-foreach.yaml`. Embed: [`examples/loop-foreach/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/loop-foreach/main.go).

### loop-repeat-until

Code, review, fix cycle with an `untilAgent` judge. The loop runs up to 5 iterations; the judge agent decides when to stop.

```yaml
# spec/v1/examples/loop-repeat-until.yaml
name: loop-repeat-until
description: Iterative code-review-fix cycle until reviewer approves.

agents:
  coder:    { description: "Developer who writes and fixes Go code." }
  reviewer: { description: "Code reviewer." }
  judge:
    description: "Decides if the review cycle is complete."
    resultSchema:
      type: object
      required: [done]
      properties:
        done: { type: boolean }
```

What it demonstrates: bounded iteration with an LLM-driven exit condition. `untilAgent` is the canonical "ask the model when to stop" primitive.

Run: `zenflow flow spec/v1/examples/loop-repeat-until.yaml`. Embed: [`examples/loop-repeat-until/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/loop-repeat-until/main.go).

### debate-until

Multi-round debate where each round runs `pro-argue` then `con-argue` sequentially, and a judge decides after each round whether to continue. `outputMode: cumulative` ensures the verdict step sees every round's output, not just the last one.

<Asciinema id="nMwrF116eEnn17bh" aria-label="zenflow flow debate-until.yaml --plan demo" />

```yaml
# spec/v1/examples/debate-until.yaml
name: debate-until
description: Multi-round debate with repeat-until loop and judge agent.

steps:
  - id: debate-rounds
    dependsOn: [setup]
    loop:
      maxIterations: 5
      untilAgent: judge
      outputMode: cumulative
      steps:
        - id: pro-argue
        - id: con-argue
```

What it demonstrates: how to combine the repeat-until loop pattern (`loop.untilAgent` with `loop.maxIterations`) with `outputMode: cumulative` so a final aggregator can see every iteration's output.

Run: `zenflow flow spec/v1/examples/debate-until.yaml`. Embed: [`examples/debate-until/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/debate-until/main.go).

### loop-bidirectional

Inside a repeat-until loop (`loop.untilAgent` + `loop.maxIterations`), the worker step sends progress to the coordinator; the coordinator forwards messages back into the loop iteration. Exercises namespaced IDs (`loop-stages.0.worker`).

```yaml
# spec/v1/examples/loop-bidirectional.yaml
name: loop-bidirectional
description: Bidirectional messaging inside a repeat-until loop.
```

What it demonstrates: the coordinator can address inner-DAG steps using either the bare name (`worker`) or the fully-qualified namespaced form (`loop-stages.0.worker`). Both route via MessageRouter delegation.

Run: `zenflow flow spec/v1/examples/loop-bidirectional.yaml`. Embed: [`examples/loop-bidirectional/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/loop-bidirectional/main.go).

### loop-parallel-bidirectional

Same shape as `loop-bidirectional`, but with a parallel `forEach` loop. Multiple iterations run concurrently; each registers its own namespaced delegation in the root router so the coordinator can address them individually as `loop-stages[0].worker`, `loop-stages[1].worker`, ....

```yaml
# spec/v1/examples/loop-parallel-bidirectional.yaml
name: loop-parallel-bidirectional
description: Parallel forEach with bidirectional messaging.
```

What it demonstrates: parallel-iteration disambiguation. The coordinator sees three concurrent `worker` instances and addresses each by index without confusion.

Run: `zenflow flow spec/v1/examples/loop-parallel-bidirectional.yaml`. Embed: [`examples/loop-parallel-bidirectional/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/loop-parallel-bidirectional/main.go).

## Composition

### include-reuse

A `deploy` sub-workflow is included once and reused for both staging and production.

```yaml
# spec/v1/examples/include-reuse.yaml
name: include-reuse
description: Build, then deploy to staging and production using a reusable deploy workflow.

includes:
  deploy: "workflows/deploy.yaml"

steps:
  - id: design
    agent: architect
  - id: build
    dependsOn: [design]
```

What it demonstrates: workflow composition. The included file's steps are namespaced under the parent step ID (e.g. `deploy-staging.run-deploy` and `deploy-production.run-deploy`), so two includes of the same file do not collide.

Run: `zenflow flow spec/v1/examples/include-reuse.yaml`. Embed: [`examples/include-reuse/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/include-reuse/main.go).

### condition

(workflow `name:` field is `condition-example`)

Conditional step skip with CEL expressions. A security audit only runs if the design mentions authentication; a performance optimisation only runs if tests pass.

```yaml
# spec/v1/examples/condition.yaml
name: condition-example
description: Workflow with conditional steps that skip based on CEL expressions.

agents:
  designer:
    resultSchema:
      type: object
      required: [features]
```

What it demonstrates: `condition:` evaluating a CEL expression against upstream step output. A skipped step satisfies its dependents' `dependsOn` as if it had succeeded.

Run: `zenflow flow spec/v1/examples/condition.yaml`. Embed: [`examples/condition/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/condition/main.go).

### full-featured

(workflow `name:` field is `full-featured-workflow`)

Demonstrates every field zenflow supports: agents, tools, file-reference prompts, model overrides, options, conditions, loops, and includes.

<Asciinema id="T6ghM70jlJEth4Ez" aria-label="zenflow flow full-featured.yaml --model google/gemini-3-flash-preview --workdir /tmp/full-feature-gemini --yolo --plan demo" />

```yaml
# spec/v1/examples/full-featured.yaml
name: full-featured-workflow
description: A workflow exercising every zenflow feature.

agents:
  planner:
    description: "Technical lead who creates implementation plans."
    prompt: "@prompts/planner.md"
    model: "bedrock/anthropic.claude-sonnet-4-6"
    disallowedTools: [bash]
    maxTurns: 10
```

What it demonstrates: the full surface area of the YAML spec. Use it as a reference for "where does field X live?" rather than as a runnable demo.

Run: `zenflow flow spec/v1/examples/full-featured.yaml`. Embed: [`examples/full-featured/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/full-featured/main.go).

## Reliability and sampling

### retries-and-sampling

Step-level and workflow-level `maxRetries` plus per-agent sampling controls (`temperature`, `topP`).

```yaml
# spec/v1/examples/retries-and-sampling.yaml
name: retries-and-sampling

agents:
  drafter:
    description: "Drafts a brief response; uses higher temperature for variety."
    temperature: 0.9
    topP: 0.95
  judge:
    description: "Decides whether the draft is acceptable; deterministic."
    temperature: 0.0

options:
  maxRetries: 1

steps:
  - id: draft
    agent: drafter
    instructions: "Write one paragraph about Go's context.Context contract."
    maxRetries: 3
  - id: review
    agent: judge
    dependsOn: [draft]
    # ... instructions elided
```

What it demonstrates: `step.maxRetries` is the per-step attempt cap after the first failure; `options.maxRetries` is the workflow-wide default used when a step omits its own. Per-agent `temperature` and `topP` let you mix stochastic drafters with deterministic judges in the same workflow.

Run: `zenflow flow spec/v1/examples/retries-and-sampling.yaml`.

## Multimodal

### context-files

Demonstrates `contextFiles` with text, image, and PDF attachments. Text files are inlined in the prompt; images and PDFs are sent as multimodal parts.

```yaml
# spec/v1/examples/context-files.yaml
name: context-files
description: Workflow demonstrating contextFiles with text, image, and PDF attachments.

steps:
  - id: analyze-text
    agent: analyst
    instructions: "Read the provided text file and summarize its content in one sentence."
    contextFiles:
      - "fixtures/context.txt"

  - id: analyze-image
    agent: analyst
    instructions: "Describe the provided image."
    contextFiles:
      - "fixtures/sample.png"

  # ... analyze-pdf and analyze-mixed steps elided
```

What it demonstrates: per-step file attachments. Image and PDF support requires a multimodal model (Gemini, Claude Sonnet). The `fixtures/` directory ships with the example so the workflow runs without additional setup.

Run: `zenflow flow spec/v1/examples/context-files.yaml`. Embed: [`examples/context-files/main.go`](https://github.com/zendev-sh/zenflow/blob/main/examples/context-files/main.go).

## See also

- [YAML reference](/yaml/) - the full schema for every field used above.
- [Architecture](./architecture.md) - how the executor, coordinator, and messaging substrate work together.
- [Quick start](./getting-started/quick-start.md) - run your first workflow in three minutes.
