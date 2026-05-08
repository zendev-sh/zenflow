# zenflow Workflow Specification v1

This directory holds the authoritative source for the zenflow YAML
workflow format. It is the contract between workflow authors, the
zenflow executor, and any third-party tool that needs to read or
validate a workflow file.

## Layout

| Path | What | Source of truth for |
| --- | --- | --- |
| [`schema.json`](schema.json) | JSON Schema (Draft 2020-12) for the workflow document. | Machine-readable validation. Mechanical tools should consume this. |
| [`spec.md`](spec.md) | Prose specification of every field, semantics, and invariants. | Human-readable contract. Goes beyond what JSON Schema can express (cycle detection rules, CEL evaluation order, namespacing of nested steps). |
| [`examples/`](examples/) | 18 reference workflows, one per primitive or feature combination. | Runnable demonstrations that the engine treats as conformance examples. |
| [`testcases/valid/`](testcases/valid/) | 18 minimal-but-valid YAML fixtures. | "Must accept" set for any conforming validator. |
| [`testcases/invalid/`](testcases/invalid/) | 41 fixtures that exercise every documented error class. | "Must reject" set with the expected error tag. |
| [`test_schema.sh`](test_schema.sh) | Bash + ajv-cli + python3 harness that runs every fixture against `schema.json`. | CI-friendly validation runner. |

## Verifying conformance

Run the schema test harness from this directory:

```bash
cd spec/v1 && bash test_schema.sh
```

The script extracts each fixture's input, validates it via
`ajv-cli`, and checks the result matches the fixture's `valid:` and
`error:` annotations. PASS, FAIL, and SKIP counts are printed at the
end. Schema-level errors (missing required fields, type mismatches,
out-of-range numeric values) are validated here; semantic errors
(cycle detection, unknown agent references, CEL syntax) require the
full Go validator and live in zenflow's regular test suite.

Prerequisites: Node.js (for `ajv-cli`), Python 3.

## Stability promise

The `v1` directory name is part of the contract. Breaking changes
to the schema will land at `v2/` alongside `v1/`; existing v1
workflows must continue to validate against `v1/schema.json` for
the lifetime of zenflow 1.x.

Backwards-compatible additions (new optional fields, new enum
values that don't conflict with existing ones) land in place. The
PR that adds them updates `spec.md`, `schema.json`, an `examples/`
file, and at least one `testcases/valid/` fixture together.

## Where to look first

- **Authoring a workflow:** start with [`examples/minimal.yaml`](examples/minimal.yaml)
  and [`examples/simple-chain.yaml`](examples/simple-chain.yaml),
  then read the relevant section of [`spec.md`](spec.md).
- **Implementing a parser:** consume [`schema.json`](schema.json)
  for structural validation, then consult `spec.md` for the
  semantic rules (CEL evaluation, namespace resolution, retry
  semantics) that JSON Schema cannot encode.
- **Adding a feature to the spec:** see the "Adding a new feature
  to the YAML spec" checklist in
  [`../../../CONTRIBUTING.md`](../../../CONTRIBUTING.md).
