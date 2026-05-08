package exec

import (
	"strings"
	"testing"
)

func TestDecidePermission_Yolo(t *testing.T) {
	p := PermissionPolicy{Yolo: true}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if !allowed || prompt || err != nil {
		t.Fatalf("yolo: got allowed=%v prompt=%v err=%v; want allowed=true prompt=false err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_Yolo_BeatsDeny(t *testing.T) {
	// Yolo is checked before Deny - matches the legacy CLI ordering
	// (and parsePermFlags rejects --yolo+--deny at parse time anyway).
	p := PermissionPolicy{Yolo: true, Deny: []string{"bash"}}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if !allowed || prompt || err != nil {
		t.Fatalf("yolo+deny: got allowed=%v prompt=%v err=%v; want yolo to win",
			allowed, prompt, err)
	}
}

func TestDecidePermission_DenyMatch(t *testing.T) {
	p := PermissionPolicy{Deny: []string{"bash", "write"}}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if allowed || prompt || err == nil {
		t.Fatalf("deny: got allowed=%v prompt=%v err=%v; want allowed=false prompt=false err!=nil",
			allowed, prompt, err)
	}
	if !strings.Contains(err.Error(), "denied by --deny") {
		t.Fatalf("deny: err message %q missing expected phrase", err.Error())
	}
}

func TestDecidePermission_AllowMatch(t *testing.T) {
	p := PermissionPolicy{Allow: []string{"read", "grep"}}
	allowed, prompt, err := DecidePermission(p, "read", nil)
	if !allowed || prompt || err != nil {
		t.Fatalf("allow: got allowed=%v prompt=%v err=%v; want allowed=true prompt=false err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_StrictNoMatch(t *testing.T) {
	p := PermissionPolicy{Strict: true, Allow: []string{"read"}}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if allowed || prompt || err == nil {
		t.Fatalf("strict: got allowed=%v prompt=%v err=%v; want allowed=false prompt=false err!=nil",
			allowed, prompt, err)
	}
	if !strings.Contains(err.Error(), "--strict mode") {
		t.Fatalf("strict: err message %q missing expected phrase", err.Error())
	}
}

func TestDecidePermission_StrictAllowMatch(t *testing.T) {
	// Strict still allows tools that ARE on the allow list.
	p := PermissionPolicy{Strict: true, Allow: []string{"read"}}
	allowed, prompt, err := DecidePermission(p, "read", nil)
	if !allowed || prompt || err != nil {
		t.Fatalf("strict+allow: got allowed=%v prompt=%v err=%v; want allowed=true prompt=false err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_AlwaysAllowHit(t *testing.T) {
	p := PermissionPolicy{}
	always := map[string]bool{"bash": true}
	allowed, prompt, err := DecidePermission(p, "bash", always)
	if !allowed || prompt || err != nil {
		t.Fatalf("alwaysAllow: got allowed=%v prompt=%v err=%v; want allowed=true prompt=false err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_AlwaysAllowMiss(t *testing.T) {
	// alwaysAllow set for a different tool - this tool still requires a prompt.
	p := PermissionPolicy{}
	always := map[string]bool{"read": true}
	allowed, prompt, err := DecidePermission(p, "bash", always)
	if allowed || !prompt || err != nil {
		t.Fatalf("alwaysAllow miss: got allowed=%v prompt=%v err=%v; want allowed=false prompt=true err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_DefaultPrompt(t *testing.T) {
	// No flags, no remembered always - caller must prompt.
	p := PermissionPolicy{}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if allowed || !prompt || err != nil {
		t.Fatalf("default: got allowed=%v prompt=%v err=%v; want allowed=false prompt=true err=nil",
			allowed, prompt, err)
	}
}

func TestDecidePermission_DenyBeforeAllow(t *testing.T) {
	// If a tool is in BOTH lists, deny wins (matches legacy ordering).
	p := PermissionPolicy{Allow: []string{"bash"}, Deny: []string{"bash"}}
	allowed, _, err := DecidePermission(p, "bash", nil)
	if allowed || err == nil {
		t.Fatalf("deny+allow: deny must win; got allowed=%v err=%v", allowed, err)
	}
}

func TestDecidePermission_NilAlwaysAllow(t *testing.T) {
	// nil map must not panic.
	p := PermissionPolicy{}
	allowed, prompt, err := DecidePermission(p, "bash", nil)
	if allowed || !prompt || err != nil {
		t.Fatalf("nil alwaysAllow: got allowed=%v prompt=%v err=%v", allowed, prompt, err)
	}
}

func TestSandboxDefaultAllow_Contents(t *testing.T) {
	// Lock the canonical safe set so accidental edits trip CI.
	want := []string{"read", "write", "grep", "glob"}
	got := SandboxDefaultAllow()
	if len(got) != len(want) {
		t.Fatalf("SandboxDefaultAllow len=%d want %d", len(got), len(want))
	}
	for i, s := range want {
		if got[i] != s {
			t.Fatalf("SandboxDefaultAllow[%d]=%q want %q", i, got[i], s)
		}
	}
	// Bash MUST NOT be in the sandbox default set.
	for _, s := range got {
		if s == "bash" {
			t.Fatalf("SandboxDefaultAllow must not contain bash")
		}
	}
	// Each call returns a fresh slice - mutating the returned slice must
	// not affect a subsequent caller.
	got[0] = "MUTATED"
	again := SandboxDefaultAllow()
	if again[0] != "read" {
		t.Fatalf("SandboxDefaultAllow returned a shared slice; got[0]=%q want %q", again[0], "read")
	}
}
