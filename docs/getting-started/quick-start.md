---
title: Quick start
description: Three runnable examples, smallest to largest. By the end of this page, you will have run a one-step workflow, a three-step linear chain, and a...
---

# Quick start

Three runnable examples, smallest to largest. By the end of this page, you will have run a one-step workflow, a three-step linear chain, and a four-step multi-agent debate.

## Install

If you have not installed zenflow yet, see [Installation](./installation.md). The shortest path on macOS or Linux:

```bash
curl -fsSL https://zenflow.sh/install.sh | sh
```

Verify:

```bash
zenflow --version
```

## Set an API key

zenflow runs on any model [goai](https://goai.sh) supports. The simplest provider to start with is Google Gemini.

```bash
export GEMINI_API_KEY=your_key_here
export ZENFLOW_MODEL=google/gemini-2.0-flash
```

`ZENFLOW_MODEL` is the default model used when `--model` is omitted; pick one your API key supports.

### Other provider snippets

```bash
# AWS Bedrock
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export ZENFLOW_MODEL=bedrock/anthropic.claude-sonnet-4-6

# Azure
export AZURE_OPENAI_API_KEY=...
export AZURE_RESOURCE_NAME=...
export ZENFLOW_MODEL=azure/gpt-5
```

For the full list, see the [Provider matrix](/concepts/agents). The CLI auto-routes by model name - `claude-sonnet-4-6` lands on Bedrock or Azure depending on which credentials are present, `gpt-5` lands on Azure OpenAI, `gemini-3-pro-preview` lands on Google direct, and so on.

## Concepts you'll see

A few terms appear in the examples below and are worth knowing up front:

- **Workflow** - a YAML file describing agents and steps.
- **Coordinator** - an LLM that watches each step, narrates progress, and routes messages between steps.
- **Mailbox** - a per-step inbox the router writes into; the agent drains it on its next turn.
- **Wake cycle** - when a new mailbox message lands, the executor signals the agent's wake channel; the agent loops, drains the mailbox, then asks the LLM what to do next.

See [Messaging](/concepts/messaging) and [Coordinator](/concepts/coordinator) for the full picture.

## Example 1 - one step

The smallest valid workflow.

```yaml
# hello.yaml
name: minimal
steps:
  - id: greet
    instructions: "Say hello."
```

Run it:

```bash
zenflow flow hello.yaml
```

Expected output (truncated):

```
▸ greet started
✓ greet completed

Hello! I'm here to help. What can I do for you today?
```

What just happened: zenflow parsed `hello.yaml`, scheduled `greet` (no dependencies), called the default model with `"Say hello."` as the user prompt, and printed the result. No agents block, no coordinator, no messaging - bare steps work.

## Example 2 - three-step chain

Linear pipeline. Each step waits for the previous one.

```yaml
# blog.yaml
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

Run it:

```bash
zenflow flow blog.yaml
```

Expected output (truncated):

```
▸ draft started
✓ draft completed
▸ review started
✓ review completed
▸ publish started
✓ publish completed

---
title: Async Programming in Go
date: <YYYY-MM-DD>
tags: [go, concurrency]
---

# Async Programming in Go
...
```

What just happened: the executor ran `draft` first (no dependencies), then `review` (waited for `draft`), then `publish` (waited for `review`). Each step's output is automatically threaded into the next step's prompt as context.

## Example 3 - multi-agent debate with a coordinator

Two debaters argue in parallel; a coordinator forwards each side's argument into the other's mailbox while they think; a judge waits for both before rendering its verdict.

```yaml
# debate-mini.yaml
name: debate-mini
description: Structured debate with coordinator-mediated argument exchange.

agents:
  pro:
    description: "Debate team arguing IN FAVOR of the proposition."
  con:
    description: "Debate team arguing AGAINST the proposition."
  judge:
    description: "Impartial judge who declares a winner with reasoning."

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

(For the full version with moderator-setup and a setup step, see [debate.yaml](https://github.com/zendev-sh/zenflow/blob/main/spec/v1/examples/debate.yaml).)

Run it:

```bash
zenflow flow debate-mini.yaml
```

Expected output (truncated):

```
▸ team-pro started
▸ team-con started
≋ pro started team-pro; con started team-con in parallel
≋ team-pro raised: junior roles are most exposed to assistant capabilities
≋ team-con countered: assistants amplify, not replace, junior judgment
✓ team-pro completed
✓ team-con completed
▸ verdict started
✓ verdict completed

Verdict: con. The pro side made a strong economic argument...
```

What just happened: `team-pro` and `team-con` started at the same time (they share no dependencies). The coordinator agent (auto-wired by the CLI) drained step lifecycle events from its mailbox, narrated progress (the lines starting with `≋`), and forwarded each team's mid-flight arguments into the other team's mailbox. When both finished, `verdict` started with both arguments threaded into its prompt.

The coordinator is what makes this a multi-agent workflow. Without it, `team-pro` and `team-con` would run in parallel but never see each other's arguments.

## Other CLI verbs

`zenflow flow` runs a fully-declared YAML workflow. There are two more verbs:

```bash
# Let the coordinator plan a workflow on the fly from a goal string.
zenflow goal "build a one-page marketing site for a launch"

# Single-agent chat with optional tool loop.
zenflow agent "review the diff" < context.txt
```

Both share the same engine and the same provider routing. See the [CLI reference](/cli/) for flags.

## Now embed in Go

The CLI is a thin wrapper around the library. Everything you ran above is also available as `zenflow.New(...).RunFlow(ctx, wf)`. The next page walks through embedding the debate workflow into a Go binary, hooking up the coordinator, reading the result, and handling errors.

[Continue to "Your first workflow"](./your-first-workflow.md)

## Troubleshooting

**`zenflow: command not found`** - the install script may not have updated your `PATH`. Re-source your shell rc, or check that `~/.local/bin` is on your `PATH`.

**`Error: GEMINI_API_KEY not set`** - export the key in the same shell before running `zenflow flow`. zenflow does not read `.env` files automatically.

**`no LLM model configured: pass --model MODEL ... or set ZENFLOW_MODEL=PROVIDER/MODEL`** - the CLI needs to know which model to use. Either export `ZENFLOW_MODEL=google/gemini-2.0-flash` (or another `PROVIDER/MODEL`) in the same shell, or pass `--model google/gemini-2.0-flash` on each command.

**`workflow validation failed: ...`** - the YAML did not match the schema. Run `zenflow validate workflow.yaml` to see the validation error without running the workflow.

**`drop reason: unknown-step`** - the coordinator addressed a step ID that does not exist. Check the step IDs in your YAML against the IDs in the drop event. For namespaced loop steps, the ID is `loop-name.iter.inner-id` (sequential) or `loop-name[iter].inner-id` (parallel).
