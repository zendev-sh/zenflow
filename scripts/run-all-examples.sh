#!/usr/bin/env bash
# run-all-examples.sh  -  Run all zenflow example workflows with a given model.
#
# Usage:
#   ./scripts/run-all-examples.sh --model google/gemini-2.5-flash
#   ./scripts/run-all-examples.sh --model bedrock/anthropic.claude-sonnet-4-6 --stream --verbose
#   ./scripts/run-all-examples.sh --model azure/DeepSeek-V3.2 --json --quiet
#
# Flags:
#   --model MODEL       Required. Provider/model (e.g., google/gemini-2.5-flash)
#   --stream            Enable streaming output
#   --verbose           Show agent LLM output
#   --plan              Show DAG diagram before execution
#   --json              NDJSON output
#   --quiet             Events only, no narration
#   --summary-only      Skip per-step narration
#   --timeout DURATION  Per-workflow timeout (default: 5m)
#   --workdir DIR       Working directory for tool execution
#   --max-retries N     Override goai retry policy
#   --trace             Enable OTel tracing
#   --skip PATTERN      Skip examples matching glob pattern (repeatable)
#   --only PATTERN      Run only examples matching glob pattern (repeatable)
#   --continue-on-fail  Continue running after a failure (default: stop on first failure)

set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ZENFLOW_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$ZENFLOW_DIR/.." && pwd)"
EXAMPLES_DIR="$ZENFLOW_DIR/spec/v1/examples"

# --- Load .env (auto-export) ---
ENV_FILE="$REPO_ROOT/.env"
if [[ -f "$ENV_FILE" ]]; then
    set -a
    # shellcheck source=/dev/null
    source "$ENV_FILE"
    set +a
    echo "=== Loaded env from $ENV_FILE ==="
else
    echo "Warning: $ENV_FILE not found  -  API keys may be missing" >&2
fi

# --- Parse arguments ---
MODEL=""
EXTRA_FLAGS=()
SKIP_PATTERNS=()
ONLY_PATTERNS=()
CONTINUE_ON_FAIL=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --model)
            MODEL="$2"; shift 2 ;;
        --stream|--verbose|--plan|--json|--quiet|--summary-only|--trace)
            EXTRA_FLAGS+=("$1"); shift ;;
        --timeout)
            EXTRA_FLAGS+=("--timeout" "$2"); shift 2 ;;
        --workdir)
            EXTRA_FLAGS+=("--workdir" "$2"); shift 2 ;;
        --max-retries)
            EXTRA_FLAGS+=("--max-retries" "$2"); shift 2 ;;
        --skip)
            SKIP_PATTERNS+=("$2"); shift 2 ;;
        --only)
            ONLY_PATTERNS+=("$2"); shift 2 ;;
        --continue-on-fail)
            CONTINUE_ON_FAIL=true; shift ;;
        *)
            echo "Unknown flag: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$MODEL" ]]; then
    echo "Error: --model is required" >&2
    echo "Usage: $0 --model provider/model [--stream] [--verbose] [--plan] [--json] [--quiet]" >&2
    exit 1
fi

# --- Build zenflow binary ---
echo "=== Building zenflow ==="
cd "$ZENFLOW_DIR"
go build -o "$ZENFLOW_DIR/zenflow-bin" ./cmd/zenflow/
ZENFLOW="$ZENFLOW_DIR/zenflow-bin"
echo "    Built: $ZENFLOW"
echo ""

# --- Collect examples ---
EXAMPLES=(
    minimal.yaml
    simple-chain.yaml
    parallel-fan-out.yaml
    condition.yaml
    loop-foreach.yaml
    loop-repeat-until.yaml
    loop-bidirectional.yaml
    loop-parallel-bidirectional.yaml
    include-reuse.yaml
    full-featured.yaml
    code-review.yaml
    research-team.yaml
    debate.yaml
    debate-until.yaml
    debate-soak.yaml
    product-launch.yaml
    context-files.yaml
    messaging-demo.yaml
    retries-and-sampling.yaml
)

# --- Filter examples ---
should_skip() {
    local name="$1"
    # Check --only filters (if any specified, must match at least one)
    if [[ ${#ONLY_PATTERNS[@]} -gt 0 ]]; then
        local matched=false
        for pat in "${ONLY_PATTERNS[@]}"; do
            if [[ "$name" == *"$pat"* ]]; then
                matched=true
                break
            fi
        done
        if [[ "$matched" == false ]]; then
            return 0 # skip
        fi
    fi
    # Check --skip filters
    for pat in "${SKIP_PATTERNS[@]}"; do
        if [[ "$name" == *"$pat"* ]]; then
            return 0 # skip
        fi
    done
    return 1 # don't skip
}

# --- Run examples ---
TOTAL=0
PASSED=0
FAILED=0
SKIPPED=0
FAILED_LIST=()

# Create a top-level scratch directory for all example runs.
SCRATCH_ROOT=$(mktemp -d /tmp/zenflow-e2e.XXXXXX)
echo "=== Scratch directory: $SCRATCH_ROOT ==="
echo ""

for example in "${EXAMPLES[@]}"; do
    if should_skip "$example"; then
        echo "--- SKIP: $example (filtered) ---"
        ((SKIPPED++))
        echo ""
        continue
    fi

    ((TOTAL++))
    EXAMPLE_NAME="${example%.yaml}"
    WORKDIR="$SCRATCH_ROOT/$EXAMPLE_NAME"
    mkdir -p "$WORKDIR"

    echo "=== [$TOTAL] $example ==="
    echo "    Command: zenflow flow $example --model $MODEL --workdir $WORKDIR ${EXTRA_FLAGS[*]:-}"

    START_TIME=$(date +%s)
    set +e
    "$ZENFLOW" flow "$EXAMPLES_DIR/$example" \
        --model "$MODEL" \
        --workdir "$WORKDIR" \
        "${EXTRA_FLAGS[@]}" 2>&1
    EXIT_CODE=$?
    set -e
    END_TIME=$(date +%s)
    ELAPSED=$((END_TIME - START_TIME))

    if [[ $EXIT_CODE -eq 0 ]]; then
        echo "    PASS (${ELAPSED}s)"
        ((PASSED++))
    else
        echo "    FAIL (exit=$EXIT_CODE, ${ELAPSED}s)"
        ((FAILED++))
        FAILED_LIST+=("$example")
        if [[ "$CONTINUE_ON_FAIL" == false ]]; then
            echo ""
            echo "=== STOPPED: $example failed. Use --continue-on-fail to keep going. ==="
            break
        fi
    fi
    echo ""
done

# --- Summary ---
echo "========================================"
echo "  Model:   $MODEL"
echo "  Scratch: $SCRATCH_ROOT"
echo "  Total:   $TOTAL"
echo "  Passed:  $PASSED"
echo "  Failed:  $FAILED"
echo "  Skipped: $SKIPPED"
if [[ ${#FAILED_LIST[@]} -gt 0 ]]; then
    echo "  Failed examples:"
    for f in "${FAILED_LIST[@]}"; do
        echo "    - $f"
    done
fi
echo "========================================"

# Show what LLM tools wrote (if anything).
ARTIFACT_COUNT=$(find "$SCRATCH_ROOT" -type f 2>/dev/null | wc -l | tr -d ' ')
if [[ "$ARTIFACT_COUNT" -gt 0 ]]; then
    echo ""
    echo "=== Artifacts written by LLM tools ($ARTIFACT_COUNT files) ==="
    find "$SCRATCH_ROOT" -type f | sort | while read -r f; do
        echo "  ${f#"$SCRATCH_ROOT"/}"
    done
fi

# Exit with failure if any test failed
if [[ $FAILED -gt 0 ]]; then
    exit 1
fi
