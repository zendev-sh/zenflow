# AGENTS.md - zenflow

Instructions for AI contributors working on the zenflow codebase.

## Commands

```bash
go build ./...                  # Build library
go build ./cmd/zenflow/         # Build CLI
go test ./...                   # Test all packages
go test -cover ./...            # Test with coverage
golangci-lint run               # Lint
./scripts/smoke-examples.sh     # Vet + build all 18 embedding examples
go test -tags e2e ./...         # Real-LLM integration tests (needs API keys)
```

## Architecture

zenflow is a declarative multi-agent workflow engine for Go. Workflows
are YAML; the engine is a DAG executor with an LLM coordinator that
narrates progress and routes events through hub-and-spoke mailboxes.
The Go SDK layer is [goai](https://goai.sh) - any provider goai
supports works as a zenflow agent backend.

```
zenflow/
├── doc.go                      # Package zenflow doc.
├── interfaces.go               # Storage/Tracer/StepIsolation/ApprovalHandler aliases (root facade)
├── workflow.go                 # Workflow / Step / StepResult / WorkflowResult / Run aliases (root facade)
├── duration.go                 # Duration alias + FormatDuration / ParseDurationStrict re-exports
├── router_facade.go            # Re-exports for internal/router/ public API
├── transcript_facade.go        # Re-exports for internal/resume/ public API
├── coord_facade.go             # Re-exports for internal/coord/ tool factories
├── agent_facade.go             # Re-exports for internal/exec/ AgentRunner ecosystem (AgentRunner, AgentResult, 22 WithRunner*, AgentHandle, sentinels)
├── orchestrator_facade.go      # Re-exports for internal/exec/ Orchestrator + 49 With* + Executor + Storage backends + parsers + coord factory + JSON coordinator + ~60 utility symbols
├── internal/
│   ├── types/                  # Event, EventType, MessageKind, Output, ProgressSink, PermissionHandler/Request (leaf, no deps on root)
│   ├── spec/                   # Workflow / Step / Run / StepResult / AgentConfig / Duration types + Storage / Tracer / StepIsolation / ApprovalHandler / ModelResolver interface contracts + parser & validator helpers
│   ├── router/                 # MessageRouter, MailboxStore, deliveryEngine (race-safe send/wake)
│   ├── resume/                 # TranscriptStore, InMemoryTranscriptStore
│   ├── coord/                  # RunnerHandle interface + 4 coord goai.Tool factories (forward_to_agent, send_message, narrate, finalize)
│   └── exec/                   # AgentRunner + Executor + Orchestrator + JSON coordinator + RunFlow/RunGoal/RunAgent + ResumeFlow + 49 With* options + Storage backends (MemoryStorage, FileStorage) + SharedMemory + parsers + validators + scheduler + CEL evaluator + portability lints + isolation default + lifecycle + prompt assembly + 16 source files moved from root
├── cmd/zenflow/
│   ├── main.go                 # CLI entrypoint
│   ├── flags.go                # Shared flag parser
│   ├── cmd_*.go                # Subcommand handlers (flow, goal, agent, validate, plan)
│   ├── orchestrator_opts.go    # Build orchestrator from CLI flags
│   ├── provider.go             # Provider resolver
│   ├── thinking.go             # --thinking flag
│   ├── trace_otel.go           # --trace flag
│   ├── signals.go              # Signal handling
│   ├── workdir.go              # Working directory helper
│   ├── permission.go           # Interactive permission gate
│   ├── stdout_sink.go          # CLI human-readable sink
│   ├── color.go                # ANSI color helpers (auto-detect TTY)
│   ├── dag/                    # DAG plan helpers
│   └── tool/                   # CLI-only IO tools (bash/read/write/glob/grep)
├── sink/
│   ├── json.go                 # NDJSON event stream
│   ├── buffered.go             # ClosableProgressSink wrapping any sink with bounded queue
│   └── lifecycle.go            # Sink lifecycle helpers (graceful shutdown / Close())
├── examples/                   # 18 //go:build example mains
└── spec/v1/
    ├── schema.json             # JSON Schema (workflow validation)
    ├── spec.md                 # Authoritative YAML specification
    ├── examples/               # 19 reference workflows
    └── testcases/              # Conformance fixtures
```

The goai SDK is consumed directly by `internal/exec/executor.go` / `internal/exec/agent_runner.go` via `github.com/zendev-sh/goai` imports - there is no separate adapter package.

## Key rules

1. **[goai](https://goai.sh) is the LLM layer.** Never reimplement the tool loop in
   zenflow. Use `goai.GenerateText` / `goai.StreamText` with
   `WithMaxSteps` and the lifecycle hooks
   (`OnBeforeToolExecute`, `OnAfterToolExecute`, `OnBeforeStep`).
   If a [goai](https://goai.sh) capability is missing, fix it in [goai](https://goai.sh) first.
2. **Hub-and-spoke only.** Peer agents never address each other
   directly. All inter-step messages flow through the coordinator's
   `forward_to_agent` tool. Adding a peer-to-peer shortcut is a
   regression.
3. **Race-safe by construction.** Delivery uses an atomic Mailbox + Wake
   pair. Every drop has a typed reason (`DropReason`). Never swallow
   a delivery silently.
4. **YAML changes ripple in lock-step.** Update `spec/v1/spec.md`
   + `spec/v1/schema.json` + at least one example in
   `spec/v1/examples/` + matching docs/yaml/<page>.md and
   docs/concepts/<topic>.md in the same PR.
5. **100% coverage on changed lines.** Coverage gate is intentional;
   never lower it. Write a real test that exercises the real path,
   not a coverage-only branch.
6. **Single Orchestrator.Close() lifecycle.** Long-lived embedders
   must `defer orch.Close()`. Examples demonstrate this; library
   API contract requires it.
7. **JSON sink is the machine surface.** `--json` output is a
   stable contract for shell consumers. New events must be
   additive; never reshape an existing event type.
8. **No backwards-compat shims pre-v1.0.** New options are born
   in their final shape (`With*` / `Without*` no-arg pairs for
   booleans, typed setters for everything else). Pre-v1.0 we
   break shapes when needed; we do not carry deprecation cycles.

## Coordinator and messaging

The coordinator is itself a `goai.AgentRunner`. Default tools:
`forward_to_agent` (route to a step by ID or namespaced
`loop-stages.<i>.<step>` form), `narrate` (push a progress event),
`finalize` (terminate the workflow with a summary). Wake cycles
default to 100; bump via `WithCoordMaxWakeCycles(n)`.

Coordinator prompt strings live in two files:
`internal/exec/coord_factory.go` holds `DefaultCoordSystemPrompt`,
and `internal/exec/coord_lib.go` holds `DefaultCoordColdStartPrompt`
+ `DefaultCoordContinuationPrompt`. Override the system slot via
`WithCoordSystemPromptSuffix(extra)`.

## Adding a new feature

1. Decide whether it belongs in the YAML spec (declarative) or the
   Orchestrator option set (programmatic).
2. For YAML: follow the lock-step list in [Key rules #4](#key-rules).
3. For Orchestrator option: add a `With<Feature>(...)` constructor in
   `options.go`, document in `docs/api/options.md`, and add a Go
   embedding example if it changes the canonical setup shape.
4. Write unit tests against mocked `provider.LanguageModel`. For
   real-LLM verification, add an `e2e` build-tagged test that gates
   on the relevant env var and runs across the [goai](https://goai.sh) provider matrix
   (Google + Bedrock + Azure).

## Testing levels

| Level | Tag | LLM | When |
| --- | --- | --- | --- |
| Unit | none | mocked | every commit, no API keys |
| Integration | `-tags e2e` | real | per-PR + nightly matrix |
| PTY | `-tags e2e` PTY harness | real | release process |
| Walkthrough | manual workflow runs | real | release process |

Race detector is on by default. SKIP-due-to-missing-env-var is **not**
PASS - report it as SKIP in test output and the PR description.
