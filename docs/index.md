---
layout: home
title: zenflow - multi-agent orchestration engine
titleTemplate: Multi-agent orchestration & workflow engine for Go
description: Multi-agent orchestration engine for Go. Declarative YAML agent workflows with an LLM coordinator, hub-and-spoke messaging, race-safe delivery, and spec-first validation. Single static binary, no runtime.

hero:
  name: zenflow
  text: Let agents flow.
  tagline: A <a href="/agent-orchestration.html" class="zf-hero-link">multi-agent orchestration</a> engine that turns declarative YAML agent workflows into a running plan. An LLM coordinator routes events through hub-and-spoke mailboxes with race-safe delivery. Runs on any provider goai supports.
  image:
    light: /zenflow-icon.png
    dark: /zenflow-icon-dark.png
    alt: zenflow ensō
  actions:
    - theme: brand
      text: Install
      link: /getting-started/installation
    - theme: alt
      text: Quick start
      link: /getting-started/quick-start
    - theme: alt
      text: View on GitHub
      link: https://github.com/zendev-sh/zenflow

features:
  - title: Declarative YAML agent workflows
    details: Multi-agent workflows expressed in a small composable spec. Steps, dependencies, parallel fan-out, conditions (CEL), loops, and includes for sub-workflow reuse. The plan ships in a YAML file you can review in a PR.
    link: /yaml/
    linkText: YAML reference
  - title: LLM coordinator with hub-and-spoke messaging
    details: A coordinator agent narrates progress, forwards events between running steps, and finalizes the run. Peer agents never address each other directly, so the topology stays auditable.
    link: /concepts/coordinator
    linkText: How the coordinator works
  - title: Race-safe Mailbox + Wake delivery
    details: Every message is delivered through a per-agent mailbox with explicit drop reasons. No silent loss, no out-of-order delivery, no leaked goroutines.
    link: /concepts/messaging
    linkText: Messaging model
  - title: Multi-provider verified
    details: Verified against Google gemini-3-pro-preview, AWS Bedrock (claude-sonnet-4-6, minimax-m2.5), and Azure (DeepSeek-V3.2, claude-sonnet-4-6, gpt-5, gpt-5.3-codex). Any model goai supports works.
    link: /concepts/agents
    linkText: How agents work
  - title: Spec-first
    details: Workflows validate against spec/v1/schema.json plus a Go validator with 40+ conformance fixtures BEFORE the first LLM call. Cycles, missing dependencies, unknown agents, malformed CEL - all rejected in milliseconds, not after a minute of model burn.
    link: /yaml/
    linkText: YAML reference
  - title: Embed anywhere
    details: CLI for one-shot runs (zenflow flow, zenflow goal, zenflow agent), or a small Go library surface (zenflow.New, Orchestrator.RunFlow) for embedding inside long-running services. Ships as a single static Go binary - no JVM, no Python interpreter, no Node runtime.
    link: /integrations/
    linkText: Integrations
---



<div class="vp-doc" style="max-width: 920px; margin: 4rem auto 2rem; padding: 0 1.5rem;">

## See it run

<Asciinema
  id="T6ghM70jlJEth4Ez"
  aria-label="zenflow flow full-featured.yaml --plan running against google/gemini-3-flash-preview, recorded from the real CLI"
/>

A real `zenflow flow spec/v1/examples/full-featured.yaml --model google/gemini-3-flash-preview --workdir /tmp/full-feature-gemini --yolo --plan` against the Gemini 3 Flash preview. The `--plan` flag prints the DAG before execution; the coordinator narrates every step boundary; four agents (planner, coder, reviewer, deployer) call `read` / `write` / `glob` / `grep` / `bash` tools to plan, implement, review, and ship a feature; the `deploy_staging` sub-workflow (loaded via `includes:`) runs after the main DAG completes.

## Why zenflow

zenflow makes two opinionated choices. The workflow is a YAML file you can review in a PR: versionable, diffable, runnable from any language that can shell out to a binary. And every inter-agent message is either delivered to a mailbox or dropped with a typed reason. There is no third option.

zenflow is built for production embedders: systems that run workflows from a queue, persist state to a database, and need an audit trail when something goes sideways. The whole engine is one Go module with a small, stable Orchestrator API.

## Three modes, one engine

```bash
zenflow flow workflow.yaml          # run a fully-declared YAML DAG
zenflow goal "ship the launch"      # let the coordinator plan a workflow on the fly
zenflow agent "review the diff"     # single-agent chat with optional tool loop
```

The library form (`zenflow.New(...).RunFlow(ctx, wf)`) is the same engine. The CLI is a thin wrapper that resolves a provider from `--model`, wires the coordinator, and prints results.

## A two-minute taste

```yaml
# debate-mini.yaml
name: debate-mini
agents:
  pro:   { description: "Argues IN FAVOR of the proposition." }
  con:   { description: "Argues AGAINST the proposition." }
  judge: { description: "Impartial judge declaring a winner." }

steps:
  - id: team-pro
    agent: pro
    instructions: "Argue: 'AI assistants will replace junior dev roles within 5 years.'"

  - id: team-con
    agent: con
    instructions: "Argue against the same proposition."

  - id: verdict
    agent: judge
    instructions: "Declare a winner with reasoning."
    dependsOn: [team-pro, team-con]
```

```bash
export GEMINI_API_KEY=...
export ZENFLOW_MODEL=google/gemini-2.0-flash
zenflow flow debate-mini.yaml
```

(For the full version with moderator-setup and a setup step, see [debate.yaml](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/examples/debate.yaml).)

The two debaters run in parallel. The coordinator forwards each side's arguments into the other's mailbox while they think. The judge waits for both before rendering its verdict.

## Where to next

- [Quick start](/getting-started/quick-start) - install, set an API key, run three mini examples.
- [Your first workflow](/getting-started/your-first-workflow) - embed zenflow in a Go binary, end to end.
- [Agent Orchestration](/agent-orchestration.html) - the runtime topology infographic with labeled message flow.
- [Architecture](/architecture) - the DAG executor, coordinator, MessageRouter, Mailbox, delivery engine.
- [Examples](/examples) - 19 reference workflows; 18 ship with Go embeddings.
- [Compare](/compare) - vs CrewAI, AutoGen, LangGraph, langchaingo.

</div>
