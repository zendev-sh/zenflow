package spec

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDuration_UnmarshalYAML(t *testing.T) {
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	input := `d: "30m"`
	var w wrapper
	if err := yaml.Unmarshal([]byte(input), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.D.D() != 30*time.Minute {
		t.Errorf("duration = %v, want 30m", w.D.D())
	}
}

func TestDuration_MarshalYAML(t *testing.T) {
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	w := wrapper{D: Duration(30 * time.Minute)}
	data, err := yaml.Marshal(&w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Schema-conformant format: only non-zero h/m/s components.
	got := string(data)
	if got != "d: 30m\n" {
		t.Errorf("yaml = %q, want %q", got, "d: 30m\n")
	}
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	type wrapper struct {
		D Duration `json:"d"`
	}
	input := `{"d":"1h"}`
	var w wrapper
	if err := json.Unmarshal([]byte(input), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.D.D() != time.Hour {
		t.Errorf("duration = %v, want 1h", w.D.D())
	}
}

func TestDuration_MarshalJSON(t *testing.T) {
	type wrapper struct {
		D Duration `json:"d"`
	}
	w := wrapper{D: Duration(time.Hour)}
	data, err := json.Marshal(&w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	want := `{"d":"1h"}`
	if got != want {
		t.Errorf("json = %q, want %q", got, want)
	}
}

func TestDuration_Zero(t *testing.T) {
	var d Duration
	if d.D() != 0 {
		t.Errorf("zero duration = %v, want 0", d.D())
	}
	if d.String() != "0s" {
		t.Errorf("zero string = %q, want %q", d.String(), "0s")
	}
}

func TestDuration_D(t *testing.T) {
	d := Duration(5 * time.Second)
	if d.D() != 5*time.Second {
		t.Errorf("D() = %v, want 5s", d.D())
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	got := FormatDuration(0)
	if got != "0s" {
		t.Errorf("FormatDuration(0) = %q, want %q", got, "0s")
	}
}

func TestFormatDuration_HoursOnly(t *testing.T) {
	got := FormatDuration(2 * time.Hour)
	if got != "2h" {
		t.Errorf("FormatDuration(2h) = %q, want %q", got, "2h")
	}
}

func TestFormatDuration_HoursMinutes(t *testing.T) {
	got := FormatDuration(1*time.Hour + 30*time.Minute)
	if got != "1h30m" {
		t.Errorf("FormatDuration(1h30m) = %q, want %q", got, "1h30m")
	}
}

func TestFormatDuration_HoursMinutesSeconds(t *testing.T) {
	got := FormatDuration(1*time.Hour + 2*time.Minute + 3*time.Second)
	if got != "1h2m3s" {
		t.Errorf("FormatDuration(1h2m3s) = %q, want %q", got, "1h2m3s")
	}
}

func TestFormatDuration_MinutesOnly(t *testing.T) {
	got := FormatDuration(45 * time.Minute)
	if got != "45m" {
		t.Errorf("FormatDuration(45m) = %q, want %q", got, "45m")
	}
}

func TestFormatDuration_SecondsOnly(t *testing.T) {
	got := FormatDuration(10 * time.Second)
	if got != "10s" {
		t.Errorf("FormatDuration(10s) = %q, want %q", got, "10s")
	}
}

func TestDuration_UnmarshalYAML_Error(t *testing.T) {
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	input := `d: "not-a-duration"`
	var w wrapper
	err := yaml.Unmarshal([]byte(input), &w)
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestDuration_UnmarshalJSON_Error(t *testing.T) {
	type wrapper struct {
		D Duration `json:"d"`
	}
	input := `{"d":"not-a-duration"}`
	var w wrapper
	err := json.Unmarshal([]byte(input), &w)
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestDuration_UnmarshalJSON_NonString(t *testing.T) {
	type wrapper struct {
		D Duration `json:"d"`
	}
	input := `{"d":42}`
	var w wrapper
	err := json.Unmarshal([]byte(input), &w)
	if err == nil {
		t.Fatal("expected error for non-string JSON value")
	}
}

func TestDuration_UnmarshalYAML_NonString(t *testing.T) {
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	// YAML number - should fail because we expect string.
	input := `d: 42`
	var w wrapper
	err := yaml.Unmarshal([]byte(input), &w)
	if err == nil {
		t.Fatal("expected error for non-string YAML value")
	}
}

func TestDuration_UnmarshalYAML_ArrayValue(t *testing.T) {
	// Array cannot be decoded to string - covers unmarshal(&s) error path (L55-57).
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	input := `d: [1, 2, 3]`
	var w wrapper
	err := yaml.Unmarshal([]byte(input), &w)
	if err == nil {
		t.Fatal("expected error for array YAML value")
	}
}

// negative durations must be rejected by ParseDurationStrict.

func TestParseDurationStrict_NegativeYAML(t *testing.T) {
	type wrapper struct {
		D Duration `yaml:"d"`
	}
	cases := []string{"-5m", "-1h", "-30s", "-1h30m", "-0s"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			raw := "d: \"" + input + "\""
			var w wrapper
			err := yaml.Unmarshal([]byte(raw), &w)
			if err == nil {
				t.Fatalf("expected error for negative duration %q in YAML, got nil", input)
			}
			if !strings.Contains(err.Error(), "must not be negative") {
				t.Errorf("error %q should mention 'must not be negative'", err.Error())
			}
		})
	}
}

func TestParseDurationStrict_NegativeJSON(t *testing.T) {
	type wrapper struct {
		D Duration `json:"d"`
	}
	cases := []string{"-5m", "-1h", "-30s"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			raw := `{"d":"` + input + `"}`
			var w wrapper
			err := json.Unmarshal([]byte(raw), &w)
			if err == nil {
				t.Fatalf("expected error for negative duration %q in JSON, got nil", input)
			}
			if !strings.Contains(err.Error(), "must not be negative") {
				t.Errorf("error %q should mention 'must not be negative'", err.Error())
			}
		})
	}
}
