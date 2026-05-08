---
title: Your first workflow
description: This page builds a real, working multi-agent workflow in Go from scratch. By the end you will have a Go binary that loads a YAML workflow, runs it...
---

# Your first workflow

This page builds a real, working multi-agent workflow in Go from scratch. By the end you will have a Go binary that loads a YAML workflow, runs it under a coordinator, prints the result, and shuts down cleanly.

If you have not run anything from the CLI yet, do [Quick start](./quick-start.md) first - it shows the same workflow without writing Go.

## What we are building

A three-step content-pipeline workflow. A `researcher` collects findings; a `writer` drafts an article from those findings; an `editor` polishes the draft. Then we add a coordinator so the writer can ask the researcher follow-up questions mid-flight.

## Step 1 - design the YAML

Save this as `pipeline.yaml`:

```yaml
name: content-pipeline
description: Research a topic, draft an article, polish the result.

agents:
  researcher:
    description: "Research analyst who investigates a topic and produces structured findings."
  writer:
    description: "Technical writer who turns research findings into a 600-word article."
  editor:
    description: "Editor who tightens prose, fixes mechanics, and adds a headline."

steps:
  - id: research
    agent: researcher
    instructions: "Research 'How edge inference is reshaping mobile apps in 2026'. Produce 5 key findings with one supporting fact each."

  - id: draft
    agent: writer
    instructions: "Write a 600-word article based on the research findings."
    dependsOn: [research]

  - id: polish
    agent: editor
    instructions: "Tighten prose, fix mechanics, add a headline. Output the final article."
    dependsOn: [draft]
```

A few things to notice:

- **`name`** identifies the workflow. It appears in event streams and is the workflow you would address with `zenflow flow pipeline.yaml`.
- **`agents`** is a map from agent name to role description. The role description is the agent's system prompt. Models, tools, and turn caps are optional per-agent overrides; we omit them here so every agent uses the orchestrator default.
- **`steps`** is the DAG. Each step has an ID, an agent reference, instructions (the user prompt), and an optional `dependsOn` edge list. The shape of `dependsOn` is the entire dependency story - there is no sequencing keyword.
- **`research`** has no dependencies, so it runs first. **`draft`** depends on `research`; the executor threads `research`'s output into `draft`'s prompt automatically. **`polish`** depends on `draft` for the same reason.

## Step 2 - wire the Go orchestrator

Save this as `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/zenflow"
)

func main() {
	// Load the YAML workflow.
	wf, err := zenflow.LoadWorkflow("pipeline.yaml")
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	// Pick a provider. Any model goai supports works here.
	llm := google.Chat("gemini-2.0-flash",
		google.WithAPIKey(os.Getenv("GEMINI_API_KEY")),
	)

	// Construct the orchestrator. WithModel is the only required option.
	orch := zenflow.New(
		zenflow.WithModel(llm),
	)
	defer orch.Close()

	// Run the workflow.
	result, err := orch.RunFlow(context.Background(), wf)
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	// Print the outcome.
	fmt.Printf("run %s: status=%s duration=%s steps=%d\n",
		result.RunID, result.Status, result.Duration, len(result.Steps))
	if result.Summary != "" {
		fmt.Println()
		fmt.Println(result.Summary)
	}
}
```

Initialise a Go module and grab the dependencies:

```bash
go mod init pipeline
go get github.com/zendev-sh/zenflow@latest
go get github.com/zendev-sh/goai/provider/google@latest
go mod tidy
```

## Step 3 - run it

```bash
export GEMINI_API_KEY=your_key_here
go run .
```

Expected output (truncated):

```
run 7c9a1e2f-...: status=completed duration=42.1s steps=3

Edge Inference Goes Mainstream: Five Shifts Reshaping Mobile in 2026
====================================================================

The mobile app stack is quietly migrating compute back onto the device...
```

Three things just happened:

1. **`zenflow.LoadWorkflow`** parsed `pipeline.yaml`, validated the schema, resolved every `dependsOn` reference, and returned a `*zenflow.Workflow`.
2. **`orch.RunFlow`** built the DAG, scheduled `research` first, threaded its output into `draft`, scheduled `draft`, threaded its output into `polish`, scheduled `polish`, and returned a `*zenflow.WorkflowResult` once everything was done.
3. **`defer orch.Close()`** drained the orchestrator's handle registry and any internal goroutines.

## Step 4 - read the result

`WorkflowResult` carries the per-step output, total token usage, and the full event log. The two most useful fields are `Status` and `Steps`:

```go
if result.Status != zenflow.StatusCompleted {
	log.Fatalf("workflow status: %s", result.Status)
}

// result.Steps is a map keyed by step ID; iteration order is not guaranteed.
// Iterate wf.Steps (from LoadWorkflow) to print in workflow definition order
// and look up each result via result.Result(step.ID).
for _, step := range wf.Steps {
	sr, ok := result.Result(step.ID)
	if !ok {
		continue
	}
	fmt.Printf("step %s: status=%s duration=%s\n",
		sr.ID, sr.Status, sr.Duration)

	if sr.Status != zenflow.StepCompleted {
		fmt.Printf("  error: %s\n", sr.Error)
		continue
	}

	fmt.Printf("  content: %s\n", sr.Content)
}
```

Possible step statuses:

- `StepCompleted` - the agent returned successfully.
- `StepFailed` - the agent returned an error or exceeded its turn budget.
- `StepSkipped` - a `condition` (CEL expression) evaluated false. Downstream `dependsOn` is satisfied as if the step had completed.
- `StepCancelled` - the run was cancelled (context cancellation, force-finalize from the coordinator).

`result.Tokens` aggregates LLM usage across every agent call in the run, including coordinator calls. Useful for budget tracking.

## Concepts you'll see in the rest of this page

The next sections mention **mailboxes** (per-step inboxes the router writes into; the agent drains its mailbox on its next turn) and **wake cycles** (when a new message lands, the executor signals the agent's wake channel; the agent loops, drains the mailbox, then asks the LLM what to do next). Cancellation interrupts both: an in-flight wake cycle returns immediately and the mailbox stops being drained. See [Messaging](/concepts/messaging) and [Coordinator](/concepts/coordinator) for the full picture.

## Step 5 - handle errors

`RunFlow` returns an error when the run cannot start (bad workflow, no provider, closed orchestrator) and `nil` when the run reaches a terminal state - even if some steps failed. To detect partial failure, check `result.Status`:

```go
result, err := orch.RunFlow(context.Background(), wf)
if err != nil {
	// Could not even start the run.
	log.Fatalf("run: %v", err)
}

switch result.Status {
case zenflow.StatusCompleted:
	fmt.Println("all good")
case zenflow.StatusPartial:
	fmt.Println("some steps failed")
	for _, sr := range result.Steps {
		if sr.Status == zenflow.StepFailed {
			fmt.Printf("  %s failed: %s\n", sr.ID, sr.Error)
		}
	}
case zenflow.StatusFailed:
	fmt.Println("workflow failed")
}
```

For long-running workflows, pass a `context.WithTimeout` or `context.WithCancel` so a stuck step does not pin the goroutine forever. Make sure your imports include `"time"` and `"context"`.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

result, err := orch.RunFlow(ctx, wf)
```

Cancellation propagates through the executor: in-flight steps see their context cancel and return with `StepCancelled`; the coordinator finalises the run; `RunFlow` returns the same `result.Status` it would have used had the run completed normally (`StatusCompleted`/`StatusPartial`/`StatusFailed`) plus `err == context.Canceled` (or `context.DeadlineExceeded`). Inspect each `result.Steps[i].Status` for `StepCancelled` to enumerate which steps stopped early.

## Step 6 - add a coordinator

So far our workflow runs sequentially - there is no coordinator and no inter-step messaging. Now we change the YAML so `research` and a parallel `outline` step start together, and a coordinator forwards research findings into the outline step's mailbox while it runs.

Update the YAML:

```yaml
name: content-pipeline
description: Research and outline in parallel, draft, polish.

agents:
  researcher: { description: "Research analyst." }
  outliner:   { description: "Section planner who builds an article outline." }
  writer:     { description: "Technical writer." }
  editor:     { description: "Editor." }

steps:
  - id: research
    agent: researcher
    instructions: "Research 'How edge inference is reshaping mobile apps in 2026'. Produce 5 key findings."

  - id: outline
    agent: outliner
    instructions: "Build a 5-section outline for an article on edge inference. Use any research findings forwarded by the coordinator."

  - id: draft
    agent: writer
    instructions: "Write a 600-word article using the research findings and the outline."
    dependsOn: [research, outline]

  - id: polish
    agent: editor
    instructions: "Tighten prose. Add a headline."
    dependsOn: [draft]
```

`research` and `outline` share no dependencies, so they run in parallel. We want the outliner to see the researcher's findings as they emerge - that is the coordinator's job.

Update the Go to install a coordinator:

```go
orch := zenflow.New(
	zenflow.WithModel(llm),
	zenflow.WithCoordinator(zenflow.NewDefaultCoordRunner(llm)),
)
defer orch.Close()
```

That is the entire change. `NewDefaultCoordRunner` returns a pre-wired `*AgentRunner` with three tools: `forward_to_agent`, `narrate`, and `finalize`. The orchestrator pushes step lifecycle events into the coordinator's mailbox; the coordinator wakes, reads the events, calls the LLM, and decides whether to forward, narrate, or finalize.

Run it again:

```bash
go run .
```

Expected output (truncated):

```
▸ research started
▸ outline started
≋ researcher started research; outliner started outline in parallel
≋ research surfaced finding 1: edge models hit 7B params on flagship phones
✓ research completed
≋ outline incorporated research findings
✓ outline completed
▸ draft started
✓ draft completed
▸ polish started
✓ polish completed

Edge Inference Goes Mainstream: ...
```

The lines starting with `≋` are coordinator narrations. The forwarding happens silently - the outline step's mailbox receives the research findings and the LLM picks them up on its next wake cycle.

## Step 7 - the Close() rationale

Every example in the repo ends with `defer orch.Close()`. Here is why.

`*Orchestrator` owns:

- An internal handle registry tracking in-flight `RunAgentAsync` invocations.
- The factory cache, if any persistent stores were configured.
- Goroutines for the progress sink wrapper, drop callback dispatch, and (when configured) the OTel tracer.

`Close()` is idempotent - safe to call from multiple goroutines, safe to call after a panic, safe to call when no work is in flight. After `Close()`, new `RunAgent` invocations are rejected with `ErrOrchestratorClosed`. `Close()` cancels every registered `RunAgentAsync` handle via `h.Cancel()` and waits up to `closeDrainDeadline` (5s) for them to finish. Synchronous `RunFlow`/`RunAgent` calls are owned by the caller's goroutine - Close does not reach them; cancel via the context if you need to abort those.

For short-lived programs like the example above, `defer orch.Close()` in `main` is enough. For long-lived embedders (an HTTP server, a queue worker), call `Close()` when shutting down the surrounding service.

## Where to next

- **More examples** - 18 of 19 reference workflows have matching Go embeddings under [examples/](https://github.com/zendev-sh/zenflow/tree/main/examples). Read [Examples](../examples.md) for the tour.
- **Architecture** - the [Architecture page](../architecture.md) walks through the executor, coordinator, MessageRouter, Mailbox, and the internal delivery engine in detail.
- **Options** - the orchestrator has 49 `With*` options for tuning concurrency, mailbox bounds, transcript caps, observability, and more. See the [Go API reference](/api/options).
- **Provider matrix** - the workflow you just wrote runs unchanged on Bedrock, Azure, OpenAI-compatible endpoints, and any other provider [goai](https://goai.sh) supports. See [Provider matrix](/concepts/agents).
