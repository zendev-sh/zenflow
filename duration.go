package zenflow

// duration.go is now a facade - Duration's type definition + every
// (un)marshal method lives in internal/spec/duration.go.

import (
	"github.com/zendev-sh/zenflow/internal/spec"
)

// Duration is re-exported from internal/spec.
type Duration = spec.Duration

// FormatDuration is re-exported from internal/spec.
var FormatDuration = spec.FormatDuration

// ParseDurationStrict is re-exported from internal/spec.
var ParseDurationStrict = spec.ParseDurationStrict
