## Summary

<!-- 1-3 sentences: what this PR does and why. -->

## Changes

<!-- Bullet list of concrete code/doc changes. Group by file or by concern. -->

-

## Test plan

<!--
Checklist of how the change was verified. zenflow holds 100% per-function
coverage as a contributor expectation (verified locally; reviewers + Codecov
diff coverage flag drops). Document any new tests + the exact commands that
show them passing. For real-LLM verification, name the providers covered and
link the run output if available.
-->

- [ ] `go build ./...` clean.
- [ ] `go vet ./...` clean.
- [ ] `go test ./... -count=1 -race` PASS.
- [ ] `go tool cover -func=cov.out | grep -v 100.0%` returns no uncovered functions.
- [ ] (If CLI/UX touched) ran a real workflow end-to-end against at least one provider.

## Related

<!-- Link to issues this closes (`Fixes #N`) or context PRs. Optional. -->
