---
title: Docker
description: zenflow ships an official multi-arch container image at ghcr.io/zendev-sh/zenflow. Each release publishes linux/amd64 and linux/arm64 variants on...
---

# Docker

zenflow ships an official multi-arch container image at **`ghcr.io/zendev-sh/zenflow`**. Each release publishes `linux/amd64` and `linux/arm64` variants on GitHub Container Registry, built from a distroless base with the static `zenflow` binary as the entrypoint.

This page covers the official image, the env-var contract for API keys, a Kubernetes Job manifest, and (at the bottom) instructions for building your own image if you need to.

## Official image

```bash
# Pull the latest release (multi-arch manifest; Docker picks amd64/arm64).
docker pull ghcr.io/zendev-sh/zenflow:latest

# Pin to a specific release for production.
docker pull ghcr.io/zendev-sh/zenflow:v0.1.0-pre
```

Available tags:

| Tag | What it points at |
| --- | --- |
| `latest` | Most recent release (skipped on prereleases). |
| `v0.1.0-pre` | Specific release. **Pin this in production.** |
| `v0.1` | Latest patch in the `v0.1.x` line. |
| `v0.1.0-pre-amd64` / `v0.1.0-pre-arm64` | Single-arch variants (rarely needed; the manifest above resolves automatically). |

> Note: floating major.minor tags (like `:v0.1`) and `:latest` are only published for stable releases (no prerelease suffix). Use the full prerelease tag (e.g., `:v0.1.0-pre`) for prerelease binaries.

What ships:

- **Distroless `nonroot` base** (`gcr.io/distroless/static-debian12:nonroot`). No shell, no package manager, no attack surface. Runs as UID 65532.
- **Static `zenflow` binary** at `/usr/local/bin/zenflow` (built with `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`).
- **Default `WORKDIR=/wd`.** Mount your workflow directory there.
- **`ENTRYPOINT` is `zenflow`.** Pass subcommands directly: `docker run ... ghcr.io/zendev-sh/zenflow:latest flow workflow.yaml`.
- **OCI labels** for `org.opencontainers.image.{title,version,source,url,revision,licenses,created}`. `docker inspect` shows everything.

## Running locally

```bash
docker run --rm \
    -e GEMINI_API_KEY \
    -e AWS_ACCESS_KEY_ID \
    -e AWS_SECRET_ACCESS_KEY \
    -e AWS_REGION=us-east-1 \
    -v "$(pwd)":/wd -w /wd \
    ghcr.io/zendev-sh/zenflow:v0.1.0-pre \
    flow workflow.yaml --json
```

Notes:

- `-e VAR` (without `=value`) forwards the env var from your shell into the container. Combine with `set -a; source .env; set +a` locally so the keys come from the gitignored `.env` file rather than being typed inline.
- `-v "$(pwd)":/wd -w /wd` mounts the current directory at the image's default workdir, so relative paths in the workflow YAML work as if you ran the binary directly.
- The image's `ENTRYPOINT` is `zenflow`, so the first positional arg is a subcommand (`flow`, `goal`, `agent`, `validate`, `plan`, `--help`, `--version`).
- For read-only mounts, swap `-v "$(pwd)":/wd` for `-v "$(pwd)":/wd:ro`. The CLI doesn't write to the workflow directory.

## Env-var contract for API keys

zenflow inherits [goai](https://goai.sh)'s provider conventions. The names you set depend on which provider your workflow uses (`WithModel` choice in YAML or programmatic config).

| Provider | Required env vars |
| --- | --- |
| Gemini direct | `GEMINI_API_KEY` (or `GOOGLE_GENERATIVE_AI_API_KEY` for some standalone Google tests) |
| AWS Bedrock | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, optionally `AWS_SESSION_TOKEN` |
| Azure OpenAI | `AZURE_OPENAI_API_KEY`, `AZURE_RESOURCE_NAME` |
| Vertex AI | Application Default Credentials via mounted `GOOGLE_APPLICATION_CREDENTIALS` JSON |

Pass them in via:

- `-e VAR=value` (one at a time, fine for a couple of keys)
- `--env-file ./prod.env` (if you have a curated env file)
- A Kubernetes `Secret` mounted as env vars (see below)

**Never `COPY .env`** into the image. Even multi-stage builds where the env file lives in the build stage will leak it via image layer history if it ever appears in the final stage. Treat secrets as runtime-only.

## Kubernetes Job manifest

For one-shot workflow runs (CI-style "run on every PR" use cases mapped onto a Kubernetes cluster), a `Job` is the right primitive.

```yaml
# workflow-job.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: zenflow-workflows
data:
  review.yaml: |
    name: pr-review
    description: Review the open PR
    agents:
      reviewer:
        description: Senior engineer reviewing code
        model: bedrock/anthropic.claude-sonnet-4-6
    steps:
      - id: review
        agent: reviewer
        instructions: |
          Read the diff from CHANGES.md and produce a structured review.
---
apiVersion: v1
kind: Secret
metadata:
  name: zenflow-provider-keys
type: Opaque
stringData:
  AWS_ACCESS_KEY_ID: AKIA...
  AWS_SECRET_ACCESS_KEY: wJal...
  AWS_REGION: us-east-1
---
apiVersion: batch/v1
kind: Job
metadata:
  name: zenflow-pr-review
spec:
  ttlSecondsAfterFinished: 3600  # auto-clean job 1h after completion
  backoffLimit: 0                # no retries; one shot
  template:
    spec:
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: zenflow
          image: ghcr.io/zendev-sh/zenflow:v0.1.0-pre
          imagePullPolicy: IfNotPresent
          args:
            - flow
            - /workflows/review.yaml
            - --json
          envFrom:
            - secretRef:
                name: zenflow-provider-keys
          volumeMounts:
            - name: workflows
              mountPath: /workflows
              readOnly: true
          resources:
            requests:
              cpu: 250m
              memory: 256Mi
            limits:
              cpu: 1
              memory: 1Gi
      volumes:
        - name: workflows
          configMap:
            name: zenflow-workflows
```

Run it:

```bash
kubectl apply -f workflow-job.yaml
kubectl logs -f job/zenflow-pr-review
```

What this manifest gets you:

- **Workflow YAML as a ConfigMap.** Editing the workflow is a `kubectl edit configmap` away. Mounting it read-only at `/workflows/` matches the Dockerfile's `WORKDIR`.
- **Secrets as env vars.** `envFrom: secretRef` injects every key in the secret as an env var with the same name. zenflow picks up `AWS_ACCESS_KEY_ID` etc. without any further config. Rotate by `kubectl edit secret` and re-running the job.
- **`backoffLimit: 0`.** zenflow workflows are usually not idempotent (LLM calls cost money, side effects matter). Don't let Kubernetes silently re-run the job on a non-zero exit.
- **`ttlSecondsAfterFinished: 3600`.** The job and its pod stick around for 1 hour after completion so you can `kubectl logs` for debugging, then auto-clean.
- **`securityContext` block.** Required by most cluster admission controllers (Pod Security Standards "restricted"). The distroless `nonroot` image runs as UID 65532 by default.
- **Resource requests/limits.** zenflow itself is light; the limits exist mostly to bound the worst case. Adjust based on your workflow's `--max-concurrency` and how chunky each step is.

## CronJob for scheduled workflows

If you want to run a workflow on a schedule (nightly QA sweep, weekly summary, etc.), wrap the same pod template in a `CronJob`:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly-summary
spec:
  schedule: "0 2 * * *"   # 02:00 UTC daily
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 5
  jobTemplate:
    spec:
      ttlSecondsAfterFinished: 86400
      backoffLimit: 0
      template:
        spec:
          # ... same pod spec as the Job example above ...
```

Notes:

- **`concurrencyPolicy: Forbid`** prevents two runs from overlapping. zenflow workflows that hit external APIs or write to shared storage usually want this.
- The `Job` history limits keep recent successful and failed runs around (`kubectl get jobs`) so you can diff event streams across days.

## Mounting persistent storage for resume

zenflow's resume path (failed-step recovery) needs a `Storage` backend that survives across runs. The CLI's default storage is file-based at `~/.zenflow/runs/` (`defaultStorageDir()` in `cmd/zenflow/main.go`). For containers, mount the storage path as a volume and set `HOME` so the default landing path becomes the volume mount:

```bash
docker run -e HOME=/data -v zenflow-data:/data ghcr.io/zendev-sh/zenflow:latest flow workflow.yaml
```

The `--storage` flag does not exist; storage location is controlled via `HOME`. For container deployments that need resume, either:

1. **Use a persistent volume.** Mount a `PersistentVolumeClaim` (e.g. at `/data`) and set `HOME=/data` so the CLI lands its `~/.zenflow/runs/` tree on the volume.
2. **Skip resume entirely.** For most batch CI-style use cases, a failed run gets re-run from scratch on the next trigger. Resume is most valuable for long-running interactive workflows.

The resume path is documented in [Resume](../concepts/resume).

## Image hygiene

A few things worth checking on any zenflow image (official or your own build):

- **No secrets in layer history.** `docker history ghcr.io/zendev-sh/zenflow:latest` should show only the build commands, never an `ENV API_KEY=...` line. If you accidentally bake a secret into a custom build, the image is compromised - rotate the secret and rebuild.
- **No source code in the runtime image.** The official image's runtime stage is distroless, so there's no shell to even check this. For custom images, verify you only copied the compiled binary from the build stage.
- **Vulnerability scan.** The official image rebuilds on each release against the latest distroless base. If you pin to an older release, re-pull periodically or wire a scanner (Trivy, Grype) into CI.

## Build your own image

The official image covers most use cases. Build your own only when you need a custom base (e.g., one that includes `git` for a workflow that shells out), an internal registry mirror, or a different Go toolchain.

The public OSS repo ships two Dockerfiles for two distinct use cases:

- [`Dockerfile`](https://github.com/zendev-sh/zenflow/blob/main/Dockerfile) is the multi-stage source-build recipe. Use this when you `docker build .` from a fresh clone: it compiles `cmd/zenflow` from source against pinned Go and distroless versions, then ships only the binary in the final stage.
- [`Dockerfile.dist`](https://github.com/zendev-sh/zenflow/blob/main/Dockerfile.dist) is the slim runtime-only recipe GoReleaser consumes when it publishes the official image (`ghcr.io/zendev-sh/zenflow:vX.Y.Z`). It assumes the binary has already been cross-compiled and just wraps it in the distroless base. You almost certainly do NOT want this one for hand builds; reach for it only if you have a separately-built binary you want to package the same way the official image does.

> Note for source-monorepo developers: these Dockerfiles are produced by the OSS release pipeline (`scripts/zenflow-export.sh prep-release`) and only appear in the exported public repo. They are not checked into the source monorepo at `zenflow/`; run the export script to materialize them locally.

For a custom build, clone the repo and use the multi-stage `Dockerfile`:

```bash
git clone https://github.com/zendev-sh/zenflow
cd zenflow

docker build -t myregistry.example/zenflow:dev \
    --build-arg VERSION=dev \
    --build-arg COMMIT=$(git rev-parse HEAD) \
    --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
```

For multi-arch builds (`linux/amd64` + `linux/arm64`):

```bash
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -t myregistry.example/zenflow:latest \
    --push .
```

The reference Dockerfile is arch-neutral (`golang:1.25-alpine` and `distroless/static-debian12` both publish multi-arch manifests; Go's cross-compile is built in). No changes needed. If you absolutely need a shell in the image (for debugging in production), swap the runtime stage's base for `gcr.io/distroless/base-debian12:debug-nonroot` - it adds busybox under `/busybox/sh`.

**Never `COPY .env`** into the image. Even multi-stage builds where the env file lives in the build stage will leak it via image layer history if it ever appears in the final stage. Treat secrets as runtime-only.
