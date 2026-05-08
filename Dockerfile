# syntax=docker/dockerfile:1.7
#
# zenflow CLI - multi-stage build, distroless final image.
# Final image is the static `zenflow` binary on a non-root distroless
# base. No shell, no package manager, just the binary.
#
# Build:
#   docker build -t ghcr.io/zendev-sh/zenflow:dev \
#     --build-arg VERSION=dev \
#     --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
#
# Run a workflow (mount the cwd as /wd, pass an LLM API key):
#   docker run --rm -e GEMINI_API_KEY -v "$PWD":/wd -w /wd \
#     ghcr.io/zendev-sh/zenflow:latest flow workflow.yaml

# ---------- Stage 1: build ----------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

# Build cache uses BuildKit's --mount=type=cache. CGO_ENABLED=0 is
# required by the static distroless base; the resulting binary has
# no glibc dependency.
ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH \
    GOFLAGS="-trimpath"

WORKDIR /src

# git is required by `go mod download` to fetch VCS-hosted modules.
# ca-certificates is required for HTTPS to module proxies + provider APIs
# during the build (the runtime image carries its own cert bundle).
RUN apk add --no-cache git ca-certificates

# Cache module downloads across builds. Copy the root + every sub-module
# go.{mod,sum} before the source tree so the module download layer is
# cached when only Go source changes. zenflow ships an in-repo sub-module
# at `observability/otel/` whose go.mod is needed for the local replace
# in the root go.mod to resolve.
COPY go.mod go.sum ./
COPY observability/otel/go.mod observability/otel/go.sum ./observability/otel/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Source. Adjacent files (.dockerignore should keep the context small).
COPY . .

# Build the CLI. Strip symbols + DWARF; inject release metadata.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build \
      -tags otel \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/zenflow \
      ./cmd/zenflow

# ---------- Stage 2: runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="zenflow"
LABEL org.opencontainers.image.description="Declarative multi-agent workflows with first-class messaging."
LABEL org.opencontainers.image.source="https://github.com/zendev-sh/zenflow"
LABEL org.opencontainers.image.url="https://zenflow.sh"
LABEL org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/zenflow /usr/local/bin/zenflow

# /wd is the conventional mount point for a workflow directory. Image
# runs as the distroless `nonroot` user (uid 65532).
WORKDIR /wd

ENTRYPOINT ["/usr/local/bin/zenflow"]
