package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// durationStringPattern enforces the spec.md surface form: optional
// negative sign + at least one whole h/m/s component. Sub-second units
// (ms, us, ns) and Go's mixed-precision forms are rejected even though
// time.ParseDuration would accept them; the schema pattern is the public
// contract.
var durationStringPattern = regexp.MustCompile(`^-?(\d+h(\d+m)?(\d+s)?|\d+m(\d+s)?|\d+s)$`)

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration {
	return time.Duration(d)
}

// String returns the duration as a human-readable string.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// FormatDuration emits only whole h/m/s components, conforming to the
// schema pattern ^-?(\d+h)?(\d+m)?(\d+s)?$. Sub-second precision is
// truncated. Negative durations are formatted with a leading "-" prefix
// and absolute-value components.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	negative := d < 0
	if negative {
		d = -d
	}
	var buf strings.Builder
	if negative {
		buf.WriteByte('-')
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		fmt.Fprintf(&buf, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&buf, "%dm", m)
	}
	// Emit "0s" when no h/m/s components were written (sub-second
	// values rounded to zero, e.g. -500ms → "-0s") so the output is
	// unambiguous and round-trippable through time.ParseDuration.
	written := buf.Len()
	if negative {
		written--
	}
	if s > 0 || written == 0 {
		fmt.Fprintf(&buf, "%ds", s)
	}
	return buf.String()
}

// MarshalYAML formats Duration as a string like "30m", "1h".
func (d Duration) MarshalYAML() (any, error) {
	return FormatDuration(time.Duration(d)), nil
}

// UnmarshalYAML parses a string like "30m", "1h", "10s" into Duration.
// Note: errors from this unmarshaler lose yaml.v3's line/field context because
// custom unmarshalers receive a sub-decoder, not the top-level one. The error
// message includes the invalid value (e.g. `invalid duration "123": ...`) which
// is sufficient for debugging in practice.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	td, err := ParseDurationStrict(s)
	if err != nil {
		return err
	}
	*d = Duration(td)
	return nil
}

// ParseDurationStrict combines time.ParseDuration with the schema
// pattern check so sub-second and mixed-precision Go forms are rejected
// at the parser boundary. Empty string is rejected (catches YAML null vs
// JSON null asymmetry uniformly). The "0s" / "0" trivial forms still
// parse to zero. Negative durations are rejected.
func ParseDurationStrict(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("invalid duration %q: must not be empty (use \"0s\" for no timeout)", s)
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("invalid duration %q: must not be negative", s)
	}
	if !durationStringPattern.MatchString(s) {
		return 0, fmt.Errorf("invalid duration %q: only whole h/m/s components are supported", s)
	}
	td, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return td, nil
}

// MarshalJSON formats Duration as a JSON string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(FormatDuration(time.Duration(d)))
}

// UnmarshalJSON parses a JSON string into Duration.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	td, err := ParseDurationStrict(s)
	if err != nil {
		return err
	}
	*d = Duration(td)
	return nil
}
