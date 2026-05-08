#!/usr/bin/env bash
#
# build-llms-full.sh  -  concatenate the OSS docs into a single
# `docs/public/llms-full.txt` artifact tuned for LLM context windows.
#
# Output shape (https://llmstxt.org / extended convention):
#   - File boundary markers `=== <relative-path> ===` between sources.
#   - YAML frontmatter blocks (`---\n...\n---`) stripped.
#   - VitePress Vue components (`<script setup>...</script>`,
#     `<style>...</style>`, `<ClientOnly>`, etc.) stripped  -  they render
#     interactive UI in the docs site but are noise to an LLM consumer.
#   - Source order is fixed (Getting Started → Architecture → Concepts
#     → CLI → YAML → API) so the artifact is byte-stable and consumers
#     can diff revisions to see what actually changed.
#
# Usage: from the OSS repo root: bash scripts/build-llms-full.sh
# Output goes to docs/public/llms-full.txt.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/docs/public/llms-full.txt"

# Ordered list of sections; each entry is either a single .md file or a
# directory globbed in lexical order.
SECTIONS=(
  "docs/getting-started"
  "docs/architecture.md"
  "docs/concepts"
  "docs/cli"
  "docs/yaml"
  "docs/api"
)

mkdir -p "$(dirname "$OUT")"
: > "$OUT"

# strip_frontmatter <file>: emit file content with leading YAML
# frontmatter (--- ... ---) removed. Idempotent on files without
# frontmatter.
strip_frontmatter() {
  awk '
    NR==1 && $0=="---" { in_fm=1; next }
    in_fm && $0=="---" { in_fm=0; next }
    !in_fm { print }
  ' "$1"
}

# strip_vue <stdin>: drop VitePress Vue blocks that render UI but
# contribute no semantic content to a documentation reader. Conservative:
# only kills <script>, <style>, and known component wrappers; leaves
# fenced code blocks (```ts ... ```) intact since they are part of the
# docs prose.
strip_vue() {
  awk '
    /^<script[[:space:]]/,/^<\/script>/  { next }
    /^<style[[:space:]]*>/,/^<\/style>/  { next }
    /^<ClientOnly>/,/^<\/ClientOnly>/    { next }
    { print }
  '
}

emit_file() {
  local path="$1"
  local rel="${path#"$ROOT"/}"
  printf '\n=== %s ===\n\n' "$rel" >> "$OUT"
  strip_frontmatter "$path" | strip_vue >> "$OUT"
}

for section in "${SECTIONS[@]}"; do
  abs="$ROOT/$section"
  if [ -f "$abs" ]; then
    emit_file "$abs"
  elif [ -d "$abs" ]; then
    while IFS= read -r -d '' file; do
      emit_file "$file"
    done < <(find "$abs" -name '*.md' -type f -print0 | sort -z)
  else
    echo "WARN: section $section not found, skipping" >&2
  fi
done

bytes=$(wc -c < "$OUT" | tr -d ' ')
echo "wrote $OUT ($bytes bytes, $(grep -c '^=== ' "$OUT") sections)"
