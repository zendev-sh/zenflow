---
title: CI/CD
description: zenflow runs as a single static Go binary. There is no daemon, no database, and no persistent state needed for CLI runs. That makes it a clean fit...
---

# CI/CD

zenflow runs as a single static Go binary. There is no daemon, no database, and no persistent state needed for CLI runs. That makes it a clean fit for any CI/CD system that lets you install a binary and set environment variables.

This page shows four full pipelines: GitHub Actions, GitLab CI, CircleCI, and Azure DevOps. Each one installs zenflow, sets API keys from secrets, runs a workflow with `--json` output captured, and uses the exit code to fail the build on workflow failure.

## What every CI integration needs

Three pieces are common to every system:

1. **Install the binary.** Either `go install github.com/zendev-sh/zenflow/cmd/zenflow@latest` (needs Go 1.25+ on the runner) or download a release artifact from `https://github.com/zendev-sh/zenflow/releases`.
2. **Set provider API keys as secrets.** zenflow reads `GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`, `AZURE_OPENAI_API_KEY`/`AZURE_RESOURCE_NAME`, etc. from the environment. Pick whichever provider matches your `WithModel` choice in the workflow.
3. **Capture the JSON event stream.** Run with `--json` so stdout is parseable NDJSON. Either save it as an artifact for later inspection, or pipe it through `jq` to extract the bits you care about (failed steps, token counts, final summary).

## GitHub Actions

```yaml
# .github/workflows/zenflow-pr.yml
name: Run zenflow workflow on PR

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

jobs:
  zenflow:
    runs-on: ubuntu-latest
    timeout-minutes: 30

    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      - name: Install zenflow
        run: go install github.com/zendev-sh/zenflow/cmd/zenflow@latest

      - name: Run workflow
        env:
          GEMINI_API_KEY: ${{ secrets.GEMINI_API_KEY }}
          AWS_ACCESS_KEY_ID: ${{ secrets.AWS_ACCESS_KEY_ID }}
          AWS_SECRET_ACCESS_KEY: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
          AWS_REGION: us-east-1
        run: |
          zenflow flow .zenflow/review.yaml --json \
            > zenflow-events.ndjson
        continue-on-error: false

      - name: Summarize result
        if: always()
        run: |
          jq -r 'select(.type == "step_end") |
            "\(.stepId) - \(.duration)"' \
            zenflow-events.ndjson
          jq -r 'select(.type == "error") |
            "FAILED: \(.stepId) - \(.error)"' \
            zenflow-events.ndjson

      - name: Upload event log
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: zenflow-events
          path: zenflow-events.ndjson
```

A few notes on this config:

- `continue-on-error: false` is the default. We spell it out to make the intent explicit: if zenflow exits non-zero, the job fails. zenflow returns `0` on success, `1` on workflow/step failure, `2` on validation/coordinator error, `3` on configuration/usage error, and `124` on watchdog timeout. All non-zero codes correctly fail the build.
- The `Summarize result` step uses `if: always()` so it runs even when the workflow step failed - that way you see the failed step in the build log instead of having to download the artifact.
- The artifact upload also runs on failure. Inspecting the full NDJSON locally with `jq` is the fastest way to debug a failed CI run.

## GitLab CI

```yaml
# .gitlab-ci.yml
stages:
  - workflow

zenflow:run:
  stage: workflow
  image: golang:1.25-alpine
  variables:
    GOFLAGS: "-mod=readonly"
  before_script:
    - apk add --no-cache jq
    - go install github.com/zendev-sh/zenflow/cmd/zenflow@latest
    - export PATH=$PATH:/root/go/bin
  script:
    - |
      zenflow flow .zenflow/review.yaml --json \
        | tee zenflow-events.ndjson
    - |
      jq -r 'select(.type == "workflow_end") |
        "Duration: \(.duration)"' \
        zenflow-events.ndjson
  artifacts:
    when: always
    paths:
      - zenflow-events.ndjson
    expire_in: 1 week
  # Provider keys come from CI/CD Settings -> Variables (masked + protected).
  # Reference them by name only; never inline values here.
```

GitLab specifics:

- The `golang:1.25-alpine` image is small (~150 MB) and gives you `go install` plus `apk` for `jq`.
- Variables marked **Masked** in GitLab project settings will not appear in build logs even if a script accidentally `echo`es them.
- `tee` lets you both capture the NDJSON to a file (for the artifact) and let it flow through to stdout (for the build log).

## CircleCI

```yaml
# .circleci/config.yml
version: 2.1

jobs:
  zenflow-run:
    docker:
      - image: cimg/go:1.25
    resource_class: medium
    steps:
      - checkout
      - run:
          name: Install zenflow
          command: |
            go install github.com/zendev-sh/zenflow/cmd/zenflow@latest
            echo 'export PATH=$PATH:$(go env GOPATH)/bin' >> $BASH_ENV
      - run:
          name: Run workflow
          command: |
            zenflow flow .zenflow/review.yaml --json \
              | tee /tmp/zenflow-events.ndjson
          environment:
            # The actual secret values are set in the CircleCI project's
            # Environment Variables UI; this block only documents the names.
            AWS_REGION: us-east-1
      - run:
          name: Show step summary
          when: always
          command: |
            jq -r 'select(.type == "step_end") |
              "\(.stepId) (\(.tokens.TotalTokens // 0) tokens)"' \
              /tmp/zenflow-events.ndjson
      - store_artifacts:
          path: /tmp/zenflow-events.ndjson
          destination: zenflow-events.ndjson

workflows:
  pr-checks:
    jobs:
      - zenflow-run:
          context:
            - llm-providers
```

CircleCI specifics:

- The `cimg/go:1.25` convenience image already has Go 1.25 and most CI tooling pre-installed.
- The **context** named `llm-providers` is where you register `GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`, etc. Contexts are reusable across projects and team-scoped, which beats per-project env vars for multi-repo orgs.
- `store_artifacts` saves the NDJSON to the build's artifact tab so you can download it from the UI.

## Azure DevOps

```yaml
# azure-pipelines.yml
trigger:
  branches:
    include: [main]
pr:
  branches:
    include: [main]

pool:
  vmImage: ubuntu-latest

variables:
  - group: llm-providers   # Variable group with secret env vars (see notes below).
  - name: GO_VERSION
    value: '1.25'

jobs:
  - job: zenflow_run
    timeoutInMinutes: 30
    steps:
      - checkout: self

      - task: GoTool@0
        displayName: Set up Go
        inputs:
          version: $(GO_VERSION)

      - script: |
          set -euo pipefail
          go install github.com/zendev-sh/zenflow/cmd/zenflow@latest
          echo "##vso[task.prependpath]$(go env GOPATH)/bin"
        displayName: Install zenflow

      - script: |
          set -euo pipefail
          zenflow flow .zenflow/review.yaml --json \
            | tee zenflow-events.ndjson
        displayName: Run workflow
        env:
          GEMINI_API_KEY: $(GEMINI_API_KEY)
          AWS_ACCESS_KEY_ID: $(AWS_ACCESS_KEY_ID)
          AWS_SECRET_ACCESS_KEY: $(AWS_SECRET_ACCESS_KEY)
          AWS_REGION: us-east-1

      - script: |
          jq -r 'select(.type == "step_end") |
            "\(.stepId) - \(.duration)"' \
            zenflow-events.ndjson
          jq -r 'select(.type == "error") |
            "FAILED: \(.stepId) - \(.error)"' \
            zenflow-events.ndjson
        displayName: Summarize result
        condition: always()

      - task: PublishPipelineArtifact@1
        displayName: Upload event log
        condition: always()
        inputs:
          targetPath: zenflow-events.ndjson
          artifact: zenflow-events
```

Azure DevOps specifics:

- **Variable groups** (`variables: - group: llm-providers`) are the right place for `GEMINI_API_KEY`, `AWS_ACCESS_KEY_ID`, etc. Mark the values **secret** in the Library UI so they are masked in logs and never echoed by `set` or `printenv`. Variable groups are project-scoped and reusable across pipelines.
- **Secrets are not auto-exported.** Azure injects regular variables as env vars automatically, but variables marked secret must be passed explicitly via the step's `env:` block (as shown above). If a step says "API key not set", the most common cause is forgetting that mapping.
- **`$(go env GOPATH)/bin` on PATH.** The `##vso[task.prependpath]` logging command is Azure's way to mutate `PATH` for subsequent steps - same shape as GitHub's `$GITHUB_PATH`.
- **`condition: always()`** runs the step even when a previous step failed - the same pattern used in the GitHub Actions example so the summary and artifact survive a failed workflow.
- **`PublishPipelineArtifact@1`** is the modern artifact task; it stores results in the run's Artifacts tab and is the recommended replacement for the older `PublishBuildArtifacts@1`.
- **Self-hosted agents** can use the same YAML; just swap `vmImage: ubuntu-latest` for `name: <your-pool>` and ensure Go 1.25 plus `jq` are installed on the agent.

## Exit code semantics

zenflow's CLI uses these exit codes (defined in `cmd/zenflow/main.go`):

| Code | Meaning | Build action |
| --- | --- | --- |
| `0` | Workflow completed successfully (all steps `completed`) | Pass |
| `1` | Workflow finished but at least one step failed (`failed` or `partial` status) | Fail |
| `2` | Validation/coordinator error (invalid YAML, schema rejection, `JSONParseError`, `CoordinatorValidationError`, `ToolNotFoundError`) | Fail (fix workflow/config) |
| `3` | CLI usage error (unknown flag, missing positional arg, `--resume`/`--plan` on goal/agent) | Fail (likely a config bug) |
| `124` | Watchdog timeout (`--timeout` exceeded; orphan goroutines killed by `os.Exit`) | Fail (consider raising timeout or splitting workflow) |

Any non-zero exit code fails the CI job by default. If you want to allow partial-success (e.g., capture artifacts but mark the build green even on workflow failure), wrap the call:

```bash
zenflow flow ... --json > out.ndjson || echo "zenflow exited $?, continuing"
```

But think hard before doing that - swallowing failures is how flaky pipelines accumulate.

## Capturing artifacts and uploading them

The two most useful things to keep around:

1. **The NDJSON event stream** (`--json` stdout). Lets you reconstruct exactly what happened without re-running. Search by step ID, filter by event type, extract token totals.
2. **Per-step output files**, if your workflow uses tools that write to disk (e.g., a step that runs `bash` and saves a report). Configure your step's `instructions` to produce these in a known directory, then upload that directory as an artifact alongside the event stream.

Avoid uploading the entire repo or the `~/.local/share/zenflow` cache. The event stream alone is small (KB to MB depending on workflow size) and contains everything diagnostic.

## Caching and concurrency

zenflow's CLI is stateless across invocations - each `zenflow flow` run is independent. There is no cache to warm.

The runtime has its own concurrency knob (`--max-concurrency`), which controls how many workflow steps run in parallel. For CI runners with limited cores, drop this to 2-3 to avoid CPU starvation. The default is 5.

If you run multiple zenflow workflows in the same job, they execute sequentially (one binary at a time) unless you background them - and there is rarely a reason to. Each workflow is its own DAG; if you need cross-workflow parallelism, fan out at the CI level (matrix builds, parallel jobs) instead.

## What about `RunGoal` (the LLM-decomposed mode)?

`zenflow goal "Do the thing"` is also one CLI invocation. The CI integration is identical - same install, same env vars, same `--json` capture. The only practical difference is that the LLM-decomposition step counts toward token usage, so a `goal` run typically uses more tokens than a hand-written `flow` workflow with the same outcome.

For repeatable CI behavior, prefer `flow` (deterministic DAG) over `goal` (LLM picks the DAG each time). Use `goal` for one-off operator commands or local prototyping.

## Common gotchas

- **Don't `set -a; source .env; set +a` in CI** - that pattern is for local dev where the `.env` file is gitignored. In CI, secrets come from the CI system's secret store, exposed as env vars on the build step. Source-ing a file you committed to the repo (with secrets in it) is a leak.
- **Pin the zenflow version** in production pipelines: `go install github.com/zendev-sh/zenflow/cmd/zenflow@v0.X.Y` instead of `@latest`. Same reason you pin any other tool.
- **Set a CLI timeout** with `--timeout 20m` on long-running workflows. Without it, a hung LLM call can block the runner until the CI system's job-level timeout fires - which is usually less informative than zenflow's own watchdog (exit `124` plus event log).
- **Don't echo the env block** in your script. CI log redaction usually masks values registered as secrets, but `printenv` or `set` will sometimes get through. Just `zenflow flow ...` directly.
