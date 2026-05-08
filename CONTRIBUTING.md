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

This repository ships under a "single-source export" model: internal
work happens in a private monorepo, and the public mirror at
`github.com/zendev-sh/zenflow` is the export target. Maintainers run
`make zenflow-export` to produce a clean staging tree (banned files
stripped, internal markers scrubbed, hygiene checks enforced) and
`make zenflow-publish` to push the exported snapshot to the public
remote.

### Manual review pass after the strip pipeline

The strip pipeline is mechanical: regex-based removal of backlog IDs
(`Z.X.Y`, `Phase N`, `Round N`, etc.), internal project codenames,
commit/PR refs, and internal paths. The exact token list lives in
`scripts/zenflow-export.sh`'s `scrub_sources` function and is the
source of truth.
Two patterns require human judgement and so the maintainer reviews
the diff before running `make zenflow-publish`:

- **Project-name vs identifier-name disambiguation.** The scrub
  matches the banned tokens only inside `//` comments and `*` doc
  bodies, so identifier names (`type Round struct`, `func roundN()`)
  are intentionally left alone. If a comment legitimately discusses a
  Go identifier whose name overlaps with a banned token, the strip
  may have introduced an empty parenthetical or a hanging punctuation
  mark. Review the staging diff at those sites and fix in the source
  repo (the canonical edit) before re-running the export.
- **Comments with intermixed semantic + marker context.** The scrub
  preserves semantic content and excises marker fragments, but a
  comment that reads `// Tweak per the namespace fix` becomes
  `// Tweak per's namespace fix` after strip - grammatically valid
  but semantically confusing. Reword the comment in the source repo.

The recommended cadence is `make zenflow-export` → `make zenflow-diff`
→ human review → fix anything ugly upstream in the source repo →
re-export → `make zenflow-publish` only when the diff reads clean.

### Tagging a release

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

This means the submodule tag is OPTIONAL on the v0.1.0-pre release path -
default `go install` works without it, and only consumers who run
`go install -tags otel` (or build official binaries from source) need
it resolvable on the proxy.

First release (`v0.1.0-pre`):

```bash
# 1. Export the staging tree:
make zenflow-export

# 2. (OPTIONAL but recommended) Prep go.mod so source rebuilds with
#    -tags otel resolve the submodule. Skipping this step still ships
#    a working release; only `go install -tags otel` from source is
#    affected, and even then GoReleaser-built distributed binaries
#    work because the tag/release sequence resolves through the proxy.
make zenflow-prep-release VERSION=v0.1.0-pre
cd "$ZENFLOW_CLONE_DIR"
git add go.mod && git commit -m "chore: drop local replace, bump otel require to v0.1.0-pre"

# 3. Push to public main:
git push origin main

# 4. (OPTIONAL, only if step 2 was run) Tag the submodule and
#    regenerate go.sum so `-tags otel` source builds resolve:
git tag observability/otel/v0.1.0-pre
git push origin observability/otel/v0.1.0-pre
go mod tidy
git add go.sum && git commit -m "chore: regenerate go.sum after submodule tag"
git push origin main

# 5. Tag the main module v0.1.0-pre on the head commit:
git tag v0.1.0-pre
git push origin v0.1.0-pre
```

GoReleaser fires from the `v0.1.0-pre` push: binaries for macOS, Linux,
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

A Developer Certificate of Origin (DCO) sign-off is required on
every commit. Add it with `git commit -s`:

```
Signed-off-by: Your Name <your.email@example.com>
```

By signing, you affirm the [DCO](https://developercertificate.org/)
- the contribution is yours to give and you license it under the
project's license.
