#!/usr/bin/env bash
# Wrapper used by `asciinema rec --command` for the demo recording.
# Loads .env so child process sees GEMINI_API_KEY, then invokes the
# CLI. Not shipped in OSS export.

set -e

# Load env (parent shell exports do not propagate into asciinema's
# headless subprocess on macOS). Walks up to repo root for .env.
if [[ -f "../.env" ]]; then
  set -a
  source ../.env
  set +a
fi

clear
sleep 0.4
printf '\033[36m$\033[0m export GEMINI_API_KEY=...\n'
sleep 0.7
printf '\033[36m$\033[0m zenflow flow spec/v1/examples/debate-until.yaml --plan --model google/gemini-2.0-flash\n'
sleep 0.5

exec ./zenflow flow spec/v1/examples/debate-until.yaml \
  --plan \
  --model google/gemini-2.0-flash \
  --max-concurrency 2
