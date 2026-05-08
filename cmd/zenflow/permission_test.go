package main

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zendev-sh/zenflow"
)

// makeReq is a tiny helper so test cases stay one-liners.
func makeReq(tool string) zenflow.PermissionRequest {
	return zenflow.PermissionRequest{
		RunID:    "run-1",
		StepID:   "step-1",
		ToolName: tool,
	}
}

// TestCliPermissionHandler_Yolo_AllowAll - `--yolo` flag short-circuits to
// allow regardless of TTY status or other flags (YOLO mode).
func TestCliPermissionHandler_Yolo_AllowAll(t *testing.T) {
	pf := permFlags{yolo: true}
	h := newCliPermissionHandler(pf, strings.NewReader(""), &bytes.Buffer{}, false /*non-TTY*/)
	for _, tool := range []string{"bash", "write", "edit", "anything"} {
		ok, err := h.RequestPermission(context.Background(), makeReq(tool))
		if err != nil {
			t.Fatalf("--yolo: tool %q got err: %v", tool, err)
		}
		if !ok {
			t.Fatalf("--yolo: tool %q expected allow=true", tool)
		}
	}
}

// TestCliPermissionHandler_AllowFlag_PreAllowsListed - `--allow bash,read`
// allows listed tools without prompt; non-listed tool prompts (here we set
// non-TTY to force deny so the test is deterministic).
func TestCliPermissionHandler_AllowFlag_PreAllowsListed(t *testing.T) {
	pf := permFlags{allow: []string{"bash", "read"}}
	h := newCliPermissionHandler(pf, strings.NewReader(""), &bytes.Buffer{}, false)

	for _, tool := range []string{"bash", "read"} {
		ok, err := h.RequestPermission(context.Background(), makeReq(tool))
		if err != nil {
			t.Fatalf("--allow: tool %q expected allow without err, got %v", tool, err)
		}
		if !ok {
			t.Fatalf("--allow: tool %q expected allow=true", tool)
		}
	}

	// Non-listed tool on non-TTY → deny.
	ok, err := h.RequestPermission(context.Background(), makeReq("write"))
	if ok {
		t.Fatalf("--allow without write: expected deny on non-TTY, got allow")
	}
	if err == nil || !strings.Contains(err.Error(), "requires permission") {
		t.Fatalf("--allow without write: expected helpful error, got %v", err)
	}
}

// TestCliPermissionHandler_DenyFlag_PreDenies - `--deny` short-circuits to
// deny + error, even on TTY.
func TestCliPermissionHandler_DenyFlag_PreDenies(t *testing.T) {
	pf := permFlags{deny: []string{"bash"}}
	h := newCliPermissionHandler(pf, strings.NewReader("y\n"), &bytes.Buffer{}, true /*TTY*/)

	ok, err := h.RequestPermission(context.Background(), makeReq("bash"))
	if ok {
		t.Fatalf("--deny: expected deny, got allow")
	}
	if err == nil || !strings.Contains(err.Error(), "denied by --deny") {
		t.Fatalf("--deny: expected denial error, got %v", err)
	}
}

// TestCliPermissionHandler_NonTTY_DefaultDeny - no flags, non-TTY → deny
// with a message instructing the user to pass --yolo / --allow.
func TestCliPermissionHandler_NonTTY_DefaultDeny(t *testing.T) {
	h := newCliPermissionHandler(permFlags{}, strings.NewReader(""), &bytes.Buffer{}, false)
	ok, err := h.RequestPermission(context.Background(), makeReq("bash"))
	if ok {
		t.Fatalf("non-TTY default: expected deny, got allow")
	}
	if err == nil || !strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("non-TTY default: expected helpful --yolo message, got %v", err)
	}
}

// TestCliPermissionHandler_TTY_AlwaysChoice - user replies "a" once, the
// handler remembers and allows the same tool on subsequent calls without
// re-reading stdin (we feed only one line; the second call must succeed
// from memory).
func TestCliPermissionHandler_TTY_AlwaysChoice(t *testing.T) {
	out := &bytes.Buffer{}
	// One "a\n" line. If the handler asks again, ReadString returns EOF and
	// we'd see a deny - which would fail the second assertion below.
	in := strings.NewReader("a\n")
	h := newCliPermissionHandler(permFlags{}, in, out, true /*TTY*/)

	ok, err := h.RequestPermission(context.Background(), makeReq("bash"))
	if err != nil || !ok {
		t.Fatalf("first prompt with 'a': expected allow, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out.String(), "Tool [bash]") {
		t.Fatalf("expected prompt printed for bash, got: %s", out.String())
	}

	// Second call - should be allowed from memory without consuming stdin.
	ok, err = h.RequestPermission(context.Background(), makeReq("bash"))
	if err != nil || !ok {
		t.Fatalf("second call after 'always': expected allow from memory, got ok=%v err=%v", ok, err)
	}
}

// TestCliPermissionHandler_TTY_YesOnce - interactive "y" allows once but
// does not promote to always; second prompt is required.
func TestCliPermissionHandler_TTY_YesOnce(t *testing.T) {
	in := strings.NewReader("y\nn\n")
	h := newCliPermissionHandler(permFlags{}, in, &bytes.Buffer{}, true)

	ok, err := h.RequestPermission(context.Background(), makeReq("bash"))
	if err != nil || !ok {
		t.Fatalf("first 'y': expected allow, got ok=%v err=%v", ok, err)
	}
	ok, err = h.RequestPermission(context.Background(), makeReq("bash"))
	if ok {
		t.Fatalf("second 'n': expected deny, got allow")
	}
	if err == nil || !strings.Contains(err.Error(), "denied by user") {
		t.Fatalf("second 'n': expected user-denial error, got %v", err)
	}
}

// TestCliPermissionHandler_Strict_DeniesUnlisted - --strict + --allow
// rejects everything not on the allow list with no prompt and no TTY check.
func TestCliPermissionHandler_Strict_DeniesUnlisted(t *testing.T) {
	pf := permFlags{strict: true, allow: []string{"read"}}
	h := newCliPermissionHandler(pf, strings.NewReader("y\n"), &bytes.Buffer{}, true)

	ok, err := h.RequestPermission(context.Background(), makeReq("bash"))
	if ok {
		t.Fatalf("--strict: expected deny for unlisted, got allow")
	}
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("--strict: expected strict-mode error, got %v", err)
	}

	// Listed tool still allowed.
	ok, err = h.RequestPermission(context.Background(), makeReq("read"))
	if err != nil || !ok {
		t.Fatalf("--strict + --allow read: expected allow, got ok=%v err=%v", ok, err)
	}
}

// TestParsePermFlags_RemovesFlagsAndParses verifies that parsePermFlags
// strips its flags from argv (so the rest can be passed to parseFlags) and
// captures values correctly.
func TestParsePermFlags_RemovesFlagsAndParses(t *testing.T) {
	// --yolo is intentionally absent here; combining --yolo with
	// --allow/--deny/--strict is now rejected (see
	// TestParsePermFlags_YoloMutuallyExclusive). This test focuses on the
	// strip/parse path with the more common --strict + --allow + --deny
	// combination.
	args := []string{"--model", "gemini-2.5-flash", "--allow", "bash, read", "--deny", "write", "--strict", "--verbose"}
	rest, pf, err := parsePermFlags(args)
	if err != nil {
		t.Fatalf("parsePermFlags err: %v", err)
	}
	if pf.yolo {
		t.Errorf("expected yolo=false")
	}
	if !pf.strict {
		t.Errorf("expected strict=true")
	}
	wantAllow := []string{"bash", "read"}
	if !equalSlice(pf.allow, wantAllow) {
		t.Errorf("allow=%v want %v", pf.allow, wantAllow)
	}
	wantDeny := []string{"write"}
	if !equalSlice(pf.deny, wantDeny) {
		t.Errorf("deny=%v want %v", pf.deny, wantDeny)
	}
	wantRest := []string{"--model", "gemini-2.5-flash", "--verbose"}
	if !equalSlice(rest, wantRest) {
		t.Errorf("rest=%v want %v", rest, wantRest)
	}
}

// TestParsePermFlags_YoloMutuallyExclusive verifies that combining --yolo
// with --allow / --deny / --strict is rejected at parse time. --yolo
// auto-approves everything; the runtime checks --yolo before --deny so a
// silent contradiction (the user thinks --deny denies, but --yolo wins)
// would otherwise slip through.
func TestParsePermFlags_YoloMutuallyExclusive(t *testing.T) {
	cases := [][]string{
		{"--yolo", "--allow", "bash"},
		{"--yolo", "--deny", "bash"},
		{"--yolo", "--strict"},
		{"--yolo", "--allow", "bash", "--deny", "write", "--strict"},
	}
	for _, args := range cases {
		_, _, err := parsePermFlags(args)
		if err == nil {
			t.Errorf("args=%v: expected error, got nil", args)
			continue
		}
		if !strings.Contains(err.Error(), "--yolo auto-approves") {
			t.Errorf("args=%v: err=%v want '--yolo auto-approves' error", args, err)
		}
	}
}

// TestParsePermFlags_MissingValue ensures missing arg yields a clear error.
func TestParsePermFlags_MissingValue(t *testing.T) {
	if _, _, err := parsePermFlags([]string{"--allow"}); err == nil {
		t.Fatalf("expected error on bare --allow")
	}
	if _, _, err := parsePermFlags([]string{"--deny"}); err == nil {
		t.Fatalf("expected error on bare --deny")
	}
}

// TestPermFlags_HasAny covers the trivial "any flag set" predicate.
func TestPermFlags_HasAny(t *testing.T) {
	if (permFlags{}).hasAny() {
		t.Errorf("empty flags should report hasAny()=false")
	}
	if !(permFlags{yolo: true}).hasAny() {
		t.Errorf("yolo should report hasAny()=true")
	}
	if !(permFlags{allow: []string{"bash"}}).hasAny() {
		t.Errorf("allow should report hasAny()=true")
	}
	if !(permFlags{deny: []string{"bash"}}).hasAny() {
		t.Errorf("deny should report hasAny()=true")
	}
	if !(permFlags{strict: true}).hasAny() {
		t.Errorf("strict should report hasAny()=true")
	}
}

// TestParsePermFlags_Sandbox_DefaultAllowList - `--sandbox` alone produces
// strict mode with exactly the 4 safe tools (read, write, grep, glob).
func TestParsePermFlags_Sandbox_DefaultAllowList(t *testing.T) {
	_, pf, err := parsePermFlags([]string{"--sandbox"})
	if err != nil {
		t.Fatalf("--sandbox: unexpected error: %v", err)
	}
	if !pf.sandbox {
		t.Errorf("--sandbox: expected sandbox=true")
	}
	if !pf.strict {
		t.Errorf("--sandbox: expected strict=true (implied)")
	}
	wantAllow := []string{"read", "write", "grep", "glob"}
	if !equalSlice(pf.allow, wantAllow) {
		t.Errorf("--sandbox: allow=%v want %v", pf.allow, wantAllow)
	}
}

// TestParsePermFlags_Sandbox_PlusAllow - `--sandbox --allow custom_tool`
// allows the 4 safe defaults + custom_tool (bash still absent).
func TestParsePermFlags_Sandbox_PlusAllow(t *testing.T) {
	_, pf, err := parsePermFlags([]string{"--sandbox", "--allow", "custom_tool"})
	if err != nil {
		t.Fatalf("--sandbox --allow custom_tool: unexpected error: %v", err)
	}
	wantAllow := []string{"read", "write", "grep", "glob", "custom_tool"}
	if !equalSlice(pf.allow, wantAllow) {
		t.Errorf("--sandbox --allow custom_tool: allow=%v want %v", pf.allow, wantAllow)
	}
	// bash must not be present.
	for _, tool := range pf.allow {
		if tool == "bash" {
			t.Errorf("--sandbox --allow custom_tool: bash appeared in allow list (sandbox must block it)")
		}
	}
}

// TestParsePermFlags_Sandbox_BashStillBlocked - `--sandbox --allow bash`
// does NOT include bash; sandbox wins over an explicit --allow bash.
func TestParsePermFlags_Sandbox_BashStillBlocked(t *testing.T) {
	_, pf, err := parsePermFlags([]string{"--sandbox", "--allow", "bash"})
	if err != nil {
		t.Fatalf("--sandbox --allow bash: unexpected error: %v", err)
	}
	for _, tool := range pf.allow {
		if tool == "bash" {
			t.Errorf("--sandbox --allow bash: bash must not appear in allow list (sandbox wins)")
		}
	}
	// The 4 safe defaults must still be present.
	wantPresent := []string{"read", "write", "grep", "glob"}
	for _, want := range wantPresent {
		found := false
		for _, got := range pf.allow {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("--sandbox --allow bash: expected %q in allow list, got %v", want, pf.allow)
		}
	}
}

// TestParsePermFlags_Sandbox_YoloConflict - `--sandbox --yolo` must return an
// error because auto-approving everything contradicts sandboxing.
func TestParsePermFlags_Sandbox_YoloConflict(t *testing.T) {
	_, _, err := parsePermFlags([]string{"--sandbox", "--yolo"})
	if err == nil {
		t.Fatalf("--sandbox --yolo: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot combine --sandbox and --yolo") {
		t.Errorf("--sandbox --yolo: unexpected error message: %v", err)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// =============================================================================
// prompt - SetReadDeadline path (stdinDeadlineReader)
// =============================================================================

// TestPrompt_DeadlinePath_CtxCancel_NoLeak verifies that when h.in satisfies
// stdinDeadlineReader (*os.File via os.Pipe), cancelling ctx:
// - returns within ~1s (not blocked on stdin indefinitely)
// - returns a "permission prompt cancelled" wrapped error
// - leaves no extra goroutine parked after the call returns
// The test injects the read-end of an os.Pipe as stdin. The pipe has no data
// written, so the read goroutine would block forever without the deadline fix.
// ctx is cancelled after 5ms; the watcher goroutine must trigger SetReadDeadline
// and unblock the reader.
func TestPrompt_DeadlinePath_CtxCancel_NoLeak(t *testing.T) {
	if runtime.GOOS == "windows" {
 // SetReadDeadline on os.Pipe doesn't reliably unblock a blocked
 // Read on Windows the way kqueue/epoll does on Unix. The
 // production fallback path (goroutine-with-documented-leak) is
 // what Windows users actually take, so a deadline-path-specific
 // goroutine assertion does not apply there.
		t.Skip("os.Pipe SetReadDeadline does not interrupt blocked Read on Windows")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pw.Close()
	defer pr.Close()

	h := newCliPermissionHandler(permFlags{}, pr, &bytes.Buffer{}, true /*TTY*/)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ok, promptErr := h.prompt(ctx, makeReq("bash"))
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("prompt took %v after ctx cancel; want <1s (goroutine leak suspected)", elapsed)
	}
	if ok {
		t.Error("expected deny on ctx cancel, got allow")
	}
	if promptErr == nil || !strings.Contains(promptErr.Error(), "permission prompt cancelled") {
		t.Errorf("err = %v; want 'permission prompt cancelled' wrap", promptErr)
	}

	// Allow the watcher goroutine to exit via the <-done case.
	runtime.Gosched()
	time.Sleep(20 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Accept up to 2 extra goroutines (runtime jitter). Anything more suggests
	// a goroutine is still parked on the pipe read.
	if after-before > 2 {
		t.Errorf("goroutine count: before=%d after=%d (delta=%d); expected <=2 extra (possible leak)",
			before, after, after-before)
	}
}

// TestPrompt_DeadlinePath_SuccessRead verifies the happy path when the reader
// satisfies stdinDeadlineReader and "y" is written to the pipe.
func TestPrompt_DeadlinePath_SuccessRead(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	go func() {
		_, _ = pw.WriteString("y\n")
		pw.Close()
	}()

	h := newCliPermissionHandler(permFlags{}, pr, &bytes.Buffer{}, true)
	ok, promptErr := h.prompt(t.Context(), makeReq("bash"))
	if promptErr != nil {
		t.Fatalf("prompt error: %v", promptErr)
	}
	if !ok {
		t.Error("expected allow for 'y' response")
	}
}

// TestPrompt_DeadlinePath_AlwaysResponse verifies the "a" (always) response
// through the deadline path promotes the tool to alwaysAllow.
func TestPrompt_DeadlinePath_AlwaysResponse(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	go func() {
		_, _ = pw.WriteString("a\n")
		pw.Close()
	}()

	h := newCliPermissionHandler(permFlags{}, pr, &bytes.Buffer{}, true)
	ok, promptErr := h.prompt(t.Context(), makeReq("bash"))
	if promptErr != nil {
		t.Fatalf("prompt error: %v", promptErr)
	}
	if !ok {
		t.Error("expected allow for 'a' response")
	}
	if !h.alwaysAllow["bash"] {
		t.Error("alwaysAllow['bash'] not set after 'a' response")
	}
}

// TestPrompt_DeadlinePath_DefaultDeny verifies empty input → deny through the
// deadline path.
func TestPrompt_DeadlinePath_DefaultDeny(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()

	go func() {
		_, _ = pw.WriteString("\n")
		pw.Close()
	}()

	h := newCliPermissionHandler(permFlags{}, pr, &bytes.Buffer{}, true)
	ok, promptErr := h.prompt(t.Context(), makeReq("bash"))
	if ok {
		t.Error("expected deny for empty response")
	}
	if promptErr == nil || !strings.Contains(promptErr.Error(), "denied by user") {
		t.Errorf("err = %v; want 'denied by user'", promptErr)
	}
}

// TestPrompt_DeadlinePath_ReadError verifies that a non-EOF read error on the
// deadline path surfaces as a "read permission response" wrapped error. We
// provoke this by closing the read-end of the pipe before prompt runs, so
// the reader goroutine immediately gets "file already closed".
func TestPrompt_DeadlinePath_ReadError(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pw.Close()

	// Close the read end now - any Read on pr will fail with a non-EOF error.
	pr.Close()

	h := newCliPermissionHandler(permFlags{}, pr, &bytes.Buffer{}, true)
	ok, promptErr := h.prompt(t.Context(), makeReq("bash"))
	if ok {
		t.Error("expected deny on read error")
	}
	if promptErr == nil || !strings.Contains(promptErr.Error(), "read permission response") {
		t.Errorf("err = %v; want 'read permission response' wrap", promptErr)
	}
}

// TestParsePermFlags_Sandbox_DenyConflict - `--sandbox --deny read` must
// return an error because "read" is in the sandbox default-allow set; the
// deny check fires first in RequestPermission, producing a silently wrong
// result. We fail loudly instead.
func TestParsePermFlags_Sandbox_DenyConflict(t *testing.T) {
	_, _, err := parsePermFlags([]string{"--sandbox", "--deny", "read"})
	if err == nil {
		t.Fatal("expected error for --sandbox --deny read, got nil")
	}
	if !strings.Contains(err.Error(), "conflicts with --sandbox defaults") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestParsePermFlags_Sandbox_DenyNonDefault - `--sandbox --deny bash` must
// succeed: bash is not in sandboxDefaultAllow (sandbox already blocks it via
// the strict+filter logic), so denying it explicitly is redundant but not
// contradictory.
func TestParsePermFlags_Sandbox_DenyNonDefault(t *testing.T) {
	_, pf, err := parsePermFlags([]string{"--sandbox", "--deny", "bash"})
	if err != nil {
		t.Fatalf("--sandbox --deny bash: expected no error, got %v", err)
	}
	// sandbox+strict must still be set.
	if !pf.sandbox {
		t.Errorf("expected sandbox=true")
	}
	if !pf.strict {
		t.Errorf("expected strict=true (implied by --sandbox)")
	}
}
