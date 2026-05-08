---
title: Installation
description: zenflow ships as a single static binary. Pick whichever method fits your workflow best - the one-command installer is the fastest path on a fresh...
---

# Installation

zenflow ships as a single static binary. Pick whichever method fits
your workflow best - the one-command installer is the fastest path
on a fresh machine.

## One-command installer (recommended)

The installer detects your OS and CPU architecture, fetches the
matching archive from the latest GitHub Release, verifies its
SHA-256 checksum, and drops the `zenflow` binary into a sensible
location.

### macOS / Linux

```bash
curl -fsSL https://zenflow.sh/install.sh | sh
```

Installs to `$HOME/.local/bin` by default. Override with
`ZENFLOW_INSTALL_DIR=/usr/local/bin sh` (read on stdin) or pin to a
specific tag with `ZENFLOW_VERSION=v0.1.0-pre sh`.

The script prints a PATH hint if `~/.local/bin` is not yet on
your `$PATH`.

If no stable release exists yet, the installer transparently falls
back to the most recent prerelease.

### Windows (PowerShell)

```powershell
iwr -useb https://zenflow.sh/install.ps1 | iex
```

Installs to `$env:LOCALAPPDATA\Programs\zenflow` by default.
Override with `$env:ZENFLOW_INSTALL_DIR` or pin to a specific tag
with `$env:ZENFLOW_VERSION = 'v0.1.0-pre'`.

## Docker

A multi-arch (linux/amd64 + linux/arm64) image ships to GitHub
Container Registry on every tagged release. The image is a
static `zenflow` binary on a distroless `nonroot` base; no
shell, no package manager.

```bash
# Pin the latest tag
docker pull ghcr.io/zendev-sh/zenflow:latest

# Run a workflow with the cwd mounted
docker run --rm \
  -e GEMINI_API_KEY \
  -e ZENFLOW_MODEL=google/gemini-2.0-flash \
  -v "$PWD":/wd -w /wd \
  ghcr.io/zendev-sh/zenflow:latest flow workflow.yaml
```

Pin a specific version with `:v0.1.0-pre` instead of `:latest`. The
working directory inside the container is `/wd`; mount your
workflow yaml + any dependencies there. Pass provider API keys
through `-e GEMINI_API_KEY` / `-e AWS_ACCESS_KEY_ID` / etc.

For CI / queue-worker / Kubernetes usage, see
[Integrations -> Docker](../integrations/docker.md).

## Homebrew (macOS / Linux)

```bash
brew install zendev-sh/tap/zenflow
```

The tap cask is auto-bumped on every release.

## go install

Useful when you already have a Go toolchain and want to track the
latest commit on `main`:

```bash
go install github.com/zendev-sh/zenflow/cmd/zenflow@latest
```

Pin a specific version with `@v0.1.0-pre` instead of `@latest`. The
binary lands in `$(go env GOPATH)/bin`. Requires Go 1.25+.

## Manual download

Pre-built archives for every supported platform live on the
[GitHub Releases](https://github.com/zendev-sh/zenflow/releases/latest)
page. Each release ships:

- `zenflow_<version>_darwin_x86_64.tar.gz`
- `zenflow_<version>_darwin_arm64.tar.gz`
- `zenflow_<version>_linux_x86_64.tar.gz`
- `zenflow_<version>_linux_arm64.tar.gz`
- `zenflow_<version>_windows_x86_64.zip`
- `zenflow_<version>_windows_arm64.zip`
- `checksums.txt`

Verify the archive against `checksums.txt` before extracting:

```bash
sha256sum -c checksums.txt --ignore-missing
```

Then extract and move the `zenflow` binary somewhere on your PATH.

## Build from source

Requires Go 1.25 or newer (matches the `go` directive in `go.mod`).

```bash
git clone https://github.com/zendev-sh/zenflow.git
cd zenflow
go build ./cmd/zenflow/
```

The resulting `./zenflow` is the same binary the release pipeline
ships, minus the version metadata baked in by the `-ldflags`
GoReleaser injects.

## Verify the install

```bash
zenflow --version
```

Should print the installed tag (or `dev` if built from source
without ldflags).

## Next

- [Quick start](./quick-start.md)
- [Your first workflow](./your-first-workflow.md)
