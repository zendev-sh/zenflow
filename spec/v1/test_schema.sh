#!/bin/bash
# Schema conformance test: validates test cases against schema.json using ajv-cli.
# Tests only schema-level validation (not custom validator rules like cycle detection).
#
# Usage: cd zenflow/spec/v1 && bash test_schema.sh

set -euo pipefail

SCHEMA="schema.json"
PASS=0
FAIL=0
SKIP=0
ERRORS=""

# Schema-level error categories (from testcases/README.md)
SCHEMA_ERRORS="missing_name missing_steps empty_steps invalid_step_id agent_missing_description negative_value"

# Extract expected error from a test case file
get_expected_error() {
    grep "error:" "$1" | head -1 | sed 's/.*error: *//'
}

get_expected_valid() {
    grep "valid:" "$1" | head -1 | sed 's/.*valid: *//'
}

# Extract input from test case and write to temp file
extract_input() {
    # Everything between "input: |" and "expected:" (YAML block scalar)
    python3 -c "
import yaml, sys, json
with open('$1') as f:
    tc = yaml.safe_load(f)
with open('$2', 'w') as f:
    f.write(tc['input'])
"
}

echo "=== Zenflow Schema Conformance Test ==="
echo ""

# --- Valid test cases ---
echo "--- Valid tests (should pass schema) ---"
for f in testcases/valid/*.yaml; do
    name=$(basename "$f")
    tmpfile=$(mktemp /tmp/zenflow_XXXXXX.yaml)
    extract_input "$f" "$tmpfile"

    result=$(npx ajv-cli@5 validate -s "$SCHEMA" -d "$tmpfile" --spec=draft2020 2>&1 || true)
    rm -f "$tmpfile"

    if echo "$result" | grep -q "valid"; then
        echo "  PASS  $name"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $name -- expected valid, got: $result"
        FAIL=$((FAIL + 1))
        ERRORS="$ERRORS\n  FAIL $name: $result"
    fi
done

echo ""

# --- Invalid test cases (schema-level only) ---
echo "--- Invalid tests (schema-level: should fail schema) ---"
for f in testcases/invalid/*.yaml; do
    name=$(basename "$f")
    expected_error=$(get_expected_error "$f")

    # Check if this is a schema-level error
    is_schema=false
    for se in $SCHEMA_ERRORS; do
        if [ "$expected_error" = "$se" ]; then
            is_schema=true
            break
        fi
    done

    if [ "$is_schema" = "false" ]; then
        echo "  SKIP  $name -- validator-level ($expected_error)"
        SKIP=$((SKIP + 1))
        continue
    fi

    tmpfile=$(mktemp /tmp/zenflow_XXXXXX.yaml)
    extract_input "$f" "$tmpfile"

    result=$(npx ajv-cli@5 validate -s "$SCHEMA" -d "$tmpfile" --spec=draft2020 2>&1 || true)
    rm -f "$tmpfile"

    if echo "$result" | grep -q "invalid"; then
        echo "  PASS  $name -- correctly rejected ($expected_error)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $name -- expected rejection ($expected_error), got: $result"
        FAIL=$((FAIL + 1))
        ERRORS="$ERRORS\n  FAIL $name: expected $expected_error rejection"
    fi
done

echo ""
echo "=== Results ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  SKIP: $SKIP (validator-level, not schema concern)"

if [ $FAIL -gt 0 ]; then
    echo ""
    echo "=== Failures ==="
    echo -e "$ERRORS"
    exit 1
fi

echo ""
echo "ALL SCHEMA TESTS PASS"
