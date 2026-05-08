package router

// DropError is the typed error returned by Router.Send when delivery
// is rejected. Callers that need to act on the specific drop reason
// (e.g. coord_tools' forward_to_agent appending a list of valid step
// IDs only on DropReasonUnknownStep) should use errors.As to extract
// the *DropError instead of substring-matching on err.Error.
//	var de *zenflow.DropError
//	if errors.As(err, &de) && de.Reason == zenflow.DropReasonUnknownStep {
// // ...
//	}
// Error returns the canonical "dropped: <reason>" string so existing
// consumers that pass err.Error through verbatim continue to work.
// Stable.
type DropError struct {
	Reason DropReason
}

// Error implements the error interface.
func (e *DropError) Error() string { return "dropped: " + e.Reason.String() }
