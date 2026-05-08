#!/usr/bin/env bash
# Wrapper used by `asciinema rec --command` for the full-featured demo.
# Loads .env so child sees GEMINI_API_KEY, then invokes the CLI.
# Not shipped in OSS export.

set -e

# Load env (parent shell exports do not propagate into asciinema's
# headless subprocess on macOS). Walks up to repo root for .env.
if [[ -f "../.env" ]]; then
  set -a
  source ../.env
  set +a
fi

# Ensure clean workdir; agents will recreate as needed.
rm -rf /tmp/full-feature-gemini
mkdir -p /tmp/full-feature-gemini

clear
sleep 0.4
printf '\033[36m$\033[0m export GEMINI_API_KEY=...\n'
sleep 0.6
printf '\033[36m$\033[0m zenflow flow spec/v1/examples/full-featured.yaml --model google/gemini-3-flash-preview --workdir /tmp/full-feature-gemini --yolo --plan\n'
sleep 0.5

exec ./zenflow flow spec/v1/examples/full-featured.yaml \
  --model google/gemini-3-flash-preview \
  --workdir /tmp/full-feature-gemini \
  --yolo \
  --plan
