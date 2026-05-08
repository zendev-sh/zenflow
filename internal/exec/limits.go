package exec

import "regexp"

// YAML workflow limits enforced by ParseWorkflow / ParseWorkflowJSON
// before ValidateWorkflow returns.
// These are library-level hard caps: any zenflow consumer benefits
// from rejecting pathological workflows early (oversized, unbounded
// nesting, injection-prone step IDs, etc.). Rejection produces
// *ValidationError; callers can emit EventError on their side.
const (
	// MaxStepsPerWorkflow caps steps in a single Workflow.Steps list
	// (top-level only; loop inner steps counted separately).
	MaxStepsPerWorkflow = 100

	// MaxNestingDepth caps the @-reference chain depth in
	// resolveChainedRef (parse.go). Sub-workflow include nesting has
	// its own cap at MaxIncludeDepth = 5; loop nesting is forbidden by
	// the parser. This constant is NOT shared with those.
	MaxNestingDepth = 20

	// MaxDescriptionChars caps Workflow.Description + Step.Instructions
	// length (guards against huge adversarial payloads).
	MaxDescriptionChars = 2000

	// MaxFileSizeBytes caps raw YAML/JSON input size passed to
	// ParseWorkflow. 1 MiB is ample for human-authored flows.
	MaxFileSizeBytes = 1 << 20

	// MaxIncludeDepth is the maximum allowed include nesting depth
	// (spec §7 line 480). runIncludeStep rejects execution when
	// IncludeDepth >= MaxIncludeDepth.
	MaxIncludeDepth = 5

	// MaxAttachmentSizeBytes caps any single contextFiles entry
	// (binary or text) so a multi-GB attachment cannot be slurped
	// into memory before the prompt assembler has a chance to bail.
	// R7A-4.
	MaxAttachmentSizeBytes = 10 * 1024 * 1024 // 10 MiB
)

// strictStepIDPattern is the canonical step ID regex: lowercase ASCII start,
// 64 chars total max, alphanum + underscore + dash. Stricter than the legacy
// stepIDPattern (which allowed mixed case and unlimited length); enforced
// additively in enforceLimits.
// Deviation from plan literal (`^[a-z][a-z0-9_]{0,63}$`): the canonical
// spec example fixtures (spec/v1/examples/*.yaml,
// spec/v1/testcases/valid/*.yaml) use dashed IDs (`analyze-image`,
// `fact-check`, etc). Rejecting dashes would invalidate published
// spec workflows. Dashes remain allowed; the 64-char cap, lowercase-
// start, and banned-special-char intent of §14.2.1 are preserved.
var strictStepIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// enforceLimits runs after ValidateWorkflow to reject workflows that exceed
// the hard caps. Uses ValidationError so existing error-handling
// paths (EventError emission at the executor) fire without changes.
func enforceLimits(wf *Workflow) error {
	if len(wf.Steps) > MaxStepsPerWorkflow {
		return &ValidationError{Message: errMaxSteps(len(wf.Steps))}
	}
	if len(wf.Description) > MaxDescriptionChars {
		return &ValidationError{Message: errMaxDescription("workflow description", len(wf.Description))}
	}
	for _, s := range wf.Steps {
		if !strictStepIDPattern.MatchString(s.ID) {
			return &ValidationError{Message: "step ID " + quote(s.ID) + " must match " + strictStepIDPattern.String()}
		}
		if len(s.Instructions) > MaxDescriptionChars {
			return &ValidationError{Message: errMaxDescription("step "+quote(s.ID)+" instructions", len(s.Instructions))}
		}
	}
	return nil
}

func quote(s string) string { return "\"" + s + "\"" }
func errMaxSteps(n int) string {
	return "workflow has too many steps (" + itoa(n) + " > max " + itoa(MaxStepsPerWorkflow) + ")"
}
func errMaxDescription(what string, n int) string {
	return what + " exceeds max " + itoa(MaxDescriptionChars) + " chars (got " + itoa(n) + ")"
}

// itoa - tiny local helper so this file has no extra imports beyond regexp.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
