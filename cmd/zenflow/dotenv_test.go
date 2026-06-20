package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	in := strings.Join([]string{
		"# a comment",
		"",
		"FOO=bar",
		"export BAZ=qux",
		`QUOTED="hello world"`,
		"SINGLE='it''s'",
		"INLINE=value # trailing comment",
		"EMPTY=",
		"SPACED =  trimmed  ",
	}, "\n")
	got, err := parseDotEnv(in)
	if err != nil {
		t.Fatalf("parseDotEnv: %v", err)
	}
	want := map[string]string{
		"FOO":    "bar",
		"BAZ":    "qux",
		"QUOTED": "hello world",
		"SINGLE": "it''s", // surrounding single quotes stripped, content verbatim
		"INLINE": "value",
		"EMPTY":  "",
		"SPACED": "trimmed",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for _, e := range got {
		w, ok := want[e.key]
		if !ok {
			t.Errorf("unexpected key %q", e.key)
			continue
		}
		if e.val != w {
			t.Errorf("%s = %q, want %q", e.key, e.val, w)
		}
	}
}

func TestParseDotEnv_Errors(t *testing.T) {
	if _, err := parseDotEnv("NOEQUALS"); err == nil || !strings.Contains(err.Error(), "missing '='") {
		t.Errorf("missing '=' err = %v", err)
	}
	if _, err := parseDotEnv("1BAD=x"); err == nil || !strings.Contains(err.Error(), "invalid variable name") {
		t.Errorf("invalid name err = %v", err)
	}
	// A single line longer than the 1 MiB scanner cap surfaces a scanner
	// error rather than silently truncating.
	huge := "K=" + strings.Repeat("v", 2*1024*1024)
	if _, err := parseDotEnv(huge); err == nil {
		t.Error("expected scanner error on oversized line")
	}
}

func TestUnquoteDotEnvValue(t *testing.T) {
	cases := map[string]string{
		`"x"`:       "x",
		`'y'`:       "y",
		`bare`:      "bare",
		`a # c`:     "a",
		`"unclosed`: `"unclosed`,
		`'`:         `'`,
		`mismatch"`: `mismatch"`,
		`v#nospace`: "v#nospace",
		`""`:        "",
	}
	for in, want := range cases {
		if got := unquoteDotEnvValue(in); got != want {
			t.Errorf("unquoteDotEnvValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsEnvKey(t *testing.T) {
	valid := []string{"A", "_x", "FOO_BAR", "x9"}
	invalid := []string{"", "9x", "a-b", "a b", "a.b"}
	for _, v := range valid {
		if !isEnvKey(v) {
			t.Errorf("isEnvKey(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if isEnvKey(v) {
			t.Errorf("isEnvKey(%q) = true, want false", v)
		}
	}
}

// recordSetenv swaps osSetenv for a recorder that does not touch the real
// environment. failKey, if non-empty, makes that key return an error.
func recordSetenv(t *testing.T, failKey string) map[string]string {
	t.Helper()
	rec := map[string]string{}
	prev := osSetenv
	osSetenv = func(k, v string) error {
		if k == failKey {
			return errors.New("boom")
		}
		rec[k] = v
		return nil
	}
	t.Cleanup(func() { osSetenv = prev })
	return rec
}

func TestLoadDotEnv_SetsMissingRespectsExisting(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("ZF_NEW=fromfile\nZF_EXISTING=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZF_EXISTING", "fromenv") // exported value must win
	rec := recordSetenv(t, "")

	loadDotEnv(cmdFlags{envFile: envPath, verbose: true})

	if rec["ZF_NEW"] != "fromfile" {
		t.Errorf("ZF_NEW not set: %v", rec)
	}
	if _, ok := rec["ZF_EXISTING"]; ok {
		t.Errorf("ZF_EXISTING should not be overridden")
	}
}

func TestLoadDotEnv_Disabled(t *testing.T) {
	rec := recordSetenv(t, "")
	loadDotEnv(cmdFlags{noDotEnv: true, envFile: "whatever"})
	if len(rec) != 0 {
		t.Errorf("expected no sets when disabled, got %v", rec)
	}
}

func TestLoadDotEnv_DefaultMissingIsSilent(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env here
	buf := captureStderr(t)
	rec := recordSetenv(t, "")
	loadDotEnv(cmdFlags{})
	if len(rec) != 0 || buf.Len() != 0 {
		t.Errorf("default missing should be silent no-op; sets=%v stderr=%q", rec, buf.String())
	}
}

func TestLoadDotEnv_ExplicitMissingReports(t *testing.T) {
	buf := captureStderr(t)
	recordSetenv(t, "")
	loadDotEnv(cmdFlags{envFile: filepath.Join(t.TempDir(), "nope.env")})
	if !strings.Contains(buf.String(), "env file not found") {
		t.Errorf("stderr = %q, want not-found report", buf.String())
	}
}

func TestLoadDotEnv_ReadErrorReports(t *testing.T) {
	// Point at a directory: ReadFile fails with a non-NotExist error.
	buf := captureStderr(t)
	recordSetenv(t, "")
	loadDotEnv(cmdFlags{envFile: t.TempDir()})
	if !strings.Contains(buf.String(), "env file:") {
		t.Errorf("stderr = %q, want read-error report", buf.String())
	}
}

func TestLoadDotEnv_ParseErrorReports(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(envPath, []byte("NOEQUALS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureStderr(t)
	recordSetenv(t, "")
	loadDotEnv(cmdFlags{envFile: envPath})
	if !strings.Contains(buf.String(), "missing '='") {
		t.Errorf("stderr = %q, want parse-error report", buf.String())
	}
}

func TestLoadDotEnv_SetenvErrorReports(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("ZF_FAILS=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureStderr(t)
	recordSetenv(t, "ZF_FAILS")
	loadDotEnv(cmdFlags{envFile: envPath})
	if !strings.Contains(buf.String(), "set ZF_FAILS") {
		t.Errorf("stderr = %q, want setenv-error report", buf.String())
	}
}

func TestParseFlags_DotEnvFlags(t *testing.T) {
	f, err := parseFlags([]string{"--env-file", "custom.env", "--no-dotenv"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.envFile != "custom.env" {
		t.Errorf("envFile = %q, want custom.env", f.envFile)
	}
	if !f.noDotEnv {
		t.Errorf("noDotEnv = false, want true")
	}
}

func TestParseFlags_EnvFileRequiresValue(t *testing.T) {
	_, err := parseFlags([]string{"--env-file"})
	if err == nil || !strings.Contains(err.Error(), "--env-file requires a path") {
		t.Fatalf("err = %v, want --env-file requires a path", err)
	}
}
