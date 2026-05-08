package zenflow

// coord_facade.go re-exports the coord goai.Tool factories.

import (
	"github.com/zendev-sh/zenflow/internal/coord"
)

// ForwardToAgentToolDef is re-exported from internal/coord.
var ForwardToAgentToolDef = coord.ForwardToAgentToolDef

// SendMessageToolDef is re-exported from internal/coord.
var SendMessageToolDef = coord.SendMessageToolDef

// NarrateToolDef is re-exported from internal/coord.
var NarrateToolDef = coord.NarrateToolDef

// FinalizeToolDef is re-exported from internal/coord.
var FinalizeToolDef = coord.FinalizeToolDef

// Coord tool argument-validation sentinels re-exported from
// internal/coord. Consumers can match against these via errors.Is to
// distinguish missing-argument failures from other tool errors.
var (
	ErrForwardTargetRequired = coord.ErrForwardTargetRequired
	ErrSendMessageEmpty      = coord.ErrSendMessageEmpty
	ErrNarrateEmpty          = coord.ErrNarrateEmpty
)
