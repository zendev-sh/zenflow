#!/usr/bin/env bash
# Regenerate AGENTS.md as a copy of CLAUDE.md (with line 1 swapped).
#
# AGENTS.md is a regular file (not a symlink) so go.mod zip / Windows
# git clones without core.symlinks=true preserve the full content. The
# bodies (line 2 onward) must stay byte-identical; the pre-commit hook
# enforces this. Run this script after editing CLAUDE.md to refresh
# AGENTS.md.
set -euo pipefail
cd "$(dirname "$0")/.."
{ printf '# AGENTS.md - zenflow\n'; tail -n +2 CLAUDE.md; } > AGENTS.md
echo "AGENTS.md regenerated from CLAUDE.md"
