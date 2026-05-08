# Examples

Eighteen runnable Go programs that show how to embed zenflow in your own
project. Each example loads the matching YAML workflow from
[`spec/v1/examples/`](../spec/v1/examples/) and runs it against a
real LLM provider.

## Prerequisites

```bash
export GEMINI_API_KEY=...   # all examples default to google/gemini-2.0-flash
```

To use a different provider, swap the `google.Chat(...)` call in any
example for `bedrock.Chat(...)` or `azure.Chat(...)` and set the
corresponding env var. See https://zenflow.sh for the full
provider matrix.

## Running

All examples build under the `example` build tag so they don't ship
with the default library build. From the `zenflow/` directory:

```bash
go run -tags example ./examples/<name>/
```

For example:

```bash
go run -tags example ./examples/minimal/
go run -tags example ./examples/simple-chain/
go run -tags example ./examples/debate/
```

To compile every example without running them:

```bash
go build -tags example ./examples/...
```

## The eighteen examples

| Example | What it shows | Coordinator |
| --- | --- | --- |
| [`minimal/`](minimal/main.go) | Smallest valid workflow - one step, no agents block. | no |
| [`simple-chain/`](simple-chain/main.go) | Linear three-step DAG via `dependsOn`. | no |
| [`parallel-fan-out/`](parallel-fan-out/main.go) | Steps with shared dependency run concurrently. | no |
| [`condition/`](condition/main.go) | CEL `if` expression skips a step based on previous output. | no |
| [`include-reuse/`](include-reuse/main.go) | Sub-workflow reused across two parents via `includes`. | no |
| [`loop-foreach/`](loop-foreach/main.go) | Parallel iteration over a dynamic array. | no |
| [`loop-repeat-until/`](loop-repeat-until/main.go) | Iteration capped by a judge agent. | no |
| [`debate/`](debate/main.go) | Two teams argue in parallel; coordinator forwards points. | yes |
| [`debate-until/`](debate-until/main.go) | Multi-round debate with judge-controlled termination. | yes |
| [`debate-soak/`](debate-soak/main.go) | Trimmed debate variant for long soak runs. | yes |
| [`code-review/`](code-review/main.go) | Parallel security and performance reviews with cross-talk. | yes |
| [`messaging-demo/`](messaging-demo/main.go) | Hub-and-spoke routing - peers never address each other directly. | yes |
| [`product-launch/`](product-launch/main.go) | Pricing, marketing, and legal teams coordinate via messages. | yes |
| [`research-team/`](research-team/main.go) | Researcher feeds writer, fact-checker, illustrator in parallel. | yes |
| [`loop-bidirectional/`](loop-bidirectional/main.go) | Repeat-until loop with bidirectional coord <-> worker messages. | yes |
| [`loop-parallel-bidirectional/`](loop-parallel-bidirectional/main.go) | Parallel forEach with namespaced per-iteration messaging. | yes |
| [`context-files/`](context-files/main.go) | Multimodal attachments - text, image, PDF context files. | yes |
| [`full-featured/`](full-featured/main.go) | Reference workflow exercising every YAML field. | yes |

The matching YAML workflows live in
[`spec/v1/examples/`](../spec/v1/examples/) - they are
provider-neutral and are the same files used by the conformance
test suite (`go test ./spec/...`).

## Smoke check

`scripts/smoke-examples.sh` runs `go vet -tags example ./examples/...`
without making any LLM calls. `go vet` compiles every package as part
of its analysis, so a clean vet implies a clean build. Use it after
editing the orchestrator options in any example to confirm it still
type-checks.

```bash
./scripts/smoke-examples.sh
```

## Copying an example into your project

Each `main.go` is self-contained - no shared helper, no internal
package. Copy the file into your own module, drop the
`//go:build example` tag, replace the path argument with whichever
YAML you want to run, and you have a working zenflow embedding.
