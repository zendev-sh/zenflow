#!/usr/bin/env bash
# smoke-examples.sh - type-check + vet every example without making an
# LLM call. Used as a pre-commit smoke gate after edits to
# examples/<name>/main.go or to the public Orchestrator API surface.
#
# Usage:
#
#   ./scripts/smoke-examples.sh           # run from zenflow/
#
# Exits non-zero if any example fails to vet or build.

set -euo pipefail

cd "$(dirname "$0")/.."

if [[ ! -d examples ]]; then
  echo "smoke-examples: no examples/ directory; nothing to do"
  exit 0
fi

echo "==> go vet -tags example ./examples/..."
go vet -tags example ./examples/...

count=$(find examples -mindepth 2 -maxdepth 2 -name main.go | wc -l | tr -d ' ')
echo "==> ${count} example(s) vetted clean (vet compiles, no separate build needed)"
