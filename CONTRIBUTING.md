# Contributing to zenflow

Contributions welcome. Here's how to get started.

## Getting started

### Prerequisites

- Go 1.25+
- `golangci-lint` (for linting)
- An LLM API key for end-to-end tests (any of: `GEMINI_API_KEY`,
  `AWS_ACCESS_KEY_ID`+`AWS_SECRET_ACCESS_KEY`+`AWS_REGION`,
  `AZURE_OPENAI_API_KEY`)

### Setup

```bash
git clone https://github.com/zendev-sh/zenflow.git
cd zenflow
go test ./...
```

### Project structure

```
zenflow/
├── *.go                    # Core engine (Orchestrator, Executor, Coordinator)
├── cmd/zenflow/            # CLI binary
│   └── tool/               # CLI-only IO tools (bash, read, write, glob, grep)
├── sink/                   # Progress sinks (stdout, JSON)
├── examples/               # 19 Go embedding examples (`//go:build example`)
├── spec/v1/
│   ├── schema.json         # JSON Schema for workflows
│   ├── spec.md             # Authoritative YAML specification
│   ├── examples/           # 19 reference workflows
│   └── testcases/          # Conformance test fixtures
└── docs/                   # User-facing documentation (VitePress)
```

The goai SDK is consumed directly by `executor.go` / `agent_runner.go` via `github.com/zendev-sh/goai` imports - there is no separate adapter package.

## Development workflow

### 1. Create a branch

```bash
git checkout -b feat/your-feature
```

### 2. Make changes

- Write code and tests.
- Run tests: `go test ./...`
- Run linter: `golangci-lint run`
- Build: `go build ./...`
- Build CLI: `go build ./cmd/zenflow/`
- Smoke-check examples: `./scripts/smoke-examples.sh`

### 3. Run end-to-end tests (optional but recommended)

End-to-end tests exercise real LLM calls against multiple providers.
They're tagged so they don't run by default:

```bash
# Single provider
GEMINI_API_KEY=... go test -tags e2e ./... -run TestE2E_Google

# Provider matrix (requires keys for each)
go test -tags e2e ./... -run TestE2E_AllProviders
```

If you don't have keys, mark the affected tests as **SKIP** in your
PR description rather than removing them.

### 4. Submit a PR

- Keep PRs focused - one feature or fix per PR.
- Include tests for new functionality (unit + integration where
  applicable).
- Update `examples/` if the public Orchestrator API changes.
- Update `spec/v1/spec.md` and `spec/v1/schema.json` together if
  the YAML surface changes.
- Update `docs/yaml/` and `docs/concepts/` to match.

## Code style

- Standard `gofmt` / `goimports` formatting.
- Follow existing patterns in the codebase.
- Prefer Go idioms: small interfaces, composition, functional options.
- Use `internal/` for implementation details that should not be
  public API.

## Adding a new feature to the YAML spec

The YAML schema is the user contract. Changes ripple through several
files; keep them in lock-step:

1. **`spec/v1/spec.md`** - prose description of the new field, its
   semantics, and examples.
2. **`spec/v1/schema.json`** - JSON Schema for validation.
3. **`spec/v1/examples/`** - at least one example workflow that
   exercises the new field.
4. **`spec/v1/testcases/`** - positive and negative test fixtures.
5. **Loader** (`parse.go`, `workflow.go`) - parsing + struct fields.
6. **Executor** (`executor.go` and friends) - runtime behavior.
7. **`docs/yaml/<page>.md`** - reference documentation.
8. **`docs/concepts/<topic>.md`** - conceptual coverage.
9. **`examples/<name>/main.go`** - Go embedding example if the
   feature affects orchestrator setup.

Run `cd spec/v1 && bash test_schema.sh` to validate.

## Testing

zenflow has four test levels. Unit tests run on every commit; higher
levels gate at `-tags e2e` and require API keys.

| Level | What | LLM | When |
| --- | --- | --- | --- |
| 1 - Unit | `go test ./...` | mocked | every commit, no API keys |
| 2 - Integration | `go test -tags e2e ./...` | real | before PR merge |
| 3 - PTY | CLI commands via PTY | real | release dance |
| 4 - Walkthrough | manual workflow runs | real | release dance |

### Test patterns

- Unit tests use mock providers; test fixtures live in `*_test.go` files alongside the code under test.
- Integration tests run against a provider matrix; gate per-provider
  on the corresponding env var.
- Race detector is on by default for everything: `-race` is a hard
  requirement.
- Coverage gate: 100% per-function. Run `go test ./... -coverprofile=cov.out && go tool cover -func=cov.out | grep -v 100.0%` and confirm zero uncovered functions before opening the PR. Do not lower the threshold; reviewers and Codecov diff coverage will flag drops.

## Releasing

zenflow ships as a multi-module Go repository: the main module is
`github.com/zendev-sh/zenflow` and there is a sibling submodule
`github.com/zendev-sh/zenflow/observability/otel` for opt-in OTel
tracing. The OTel wiring sits behind a `//go:build otel` tag in
`cmd/zenflow/trace_otel.go`, so default builds (plain `go install
github.com/zendev-sh/zenflow/cmd/zenflow@<version>`) skip the
submodule entirely and `--trace` is a runtime no-op. Distributed
binaries (Homebrew, GoReleaser releases, GHCR Docker images) are
built with `-tags otel` so end users get full `--trace` support out
of the box.

This means the submodule tag is OPTIONAL on the release path -
default `go install` works without it, and only consumers who run
`go install -tags otel` (or build official binaries from source) need
it resolvable on the proxy.

### Tagging a release

Releases are cut from `main`. Replace `<version>` with the release tag
(for example `v0.2.0`):

```bash
# 1. (OPTIONAL but recommended) Tag the submodule first and regenerate
#    go.sum so `-tags otel` source builds resolve the new version:
git tag observability/otel/<version>
git push origin observability/otel/<version>
go mod tidy
git add go.sum && git commit -m "chore: regenerate go.sum after submodule tag"
git push origin main

# 2. Tag the main module on the head commit:
git tag <version>
git push origin <version>
```

GoReleaser fires from the `<version>` push: binaries for macOS, Linux,
and Windows (amd64 + arm64) built with `-tags otel`, Homebrew cask
auto-PR, GHCR Docker images (also `-tags otel`). The docs site
rebuilds on the main push.

Subsequent releases follow the same pattern. The submodule tag stays
valid until the submodule itself changes; bump it together with the
main module only when the submodule receives a real change.

## Reporting issues

- Use [GitHub Issues](https://github.com/zendev-sh/zenflow/issues).
- Include: Go version, provider, minimal reproduction (workflow YAML
  + invocation).
- For API errors, include the HTTP status code and provider message
  (redact API keys).
- For coordinator hangs or message-routing surprises, attach the
  output of `zenflow flow ... --json` or the JSON sink output if
  embedding via library.

## License

By contributing, you agree that your contributions will be licensed
under the [Apache 2.0 License](LICENSE).
