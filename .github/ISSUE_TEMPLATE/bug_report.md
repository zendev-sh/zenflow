---
name: Bug report
about: Report unexpected behaviour from the zenflow engine, CLI, or library.
title: "[bug] "
labels: [bug, triage]
---

## What happened

<!-- One-paragraph description of the unexpected behaviour. -->

## What you expected

<!-- What did you expect zenflow to do instead? -->

## How to reproduce

<!-- Smallest workflow YAML / Go embedding snippet that triggers the issue. -->

```yaml
# minimal.yaml
name: repro
agents: {}
steps:
  - id: bug
    instructions: "..."
```

```bash
zenflow flow minimal.yaml --model <provider/model-id> --json
```

## Environment

- zenflow version: <!-- `zenflow --version` -->
- Go version: <!-- `go version` -->
- OS / arch: <!-- `uname -sm` -->
- Provider: <!-- gemini / bedrock / azure-openai / azure-anthropic / azure-deployment / ... -->
- Model: <!-- e.g. `gemini-3-pro-preview`, `anthropic.claude-sonnet-4-6`, `gpt-5` -->

## Logs

<!--
Paste the full `--json` event stream or stderr around the failure if you can.
Redact API keys / secrets first.
-->

```text
```

## Anything else?

<!-- Workarounds tried, related issues, anything that helps triage. -->
