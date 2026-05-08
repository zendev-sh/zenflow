package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zendev-sh/zenflow/internal/spec"
)

var stepIDPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// filepathAbs wraps filepath.Abs for test injection. In production this is
// filepath.Abs; tests can override to simulate os.Getwd failures.
var filepathAbs = filepath.Abs

// ErrRefPathEscape is returned (wrapped) when an @-ref path resolves to
// a location outside the workflow's BaseDir. Callers may match via
// errors.Is to distinguish path-traversal rejections from generic
// ref-load failures (stat/read errors, size cap).
// Stable.
var ErrRefPathEscape = errors.New("zenflow: ref path escapes workflow directory")

// LoadWorkflow reads a YAML file, parses it, validates, and resolves @ refs.
func LoadWorkflow(path string) (*Workflow, error) {
	// R7A-3: stat-before-read so a multi-GB hostile file is rejected
	// without ever being slurped into memory. Parsers below also enforce
	// MaxFileSizeBytes against the loaded byte slice, but that check fires
	// AFTER os.ReadFile has already allocated the full payload.
	info, err := os.Stat(path)
	if err != nil {
		return nil, &ValidationError{Message: err.Error()}
	}
	if info.Size() > MaxFileSizeBytes {
		return nil, &ValidationError{Message: fmt.Sprintf("workflow file %q exceeds %d bytes (got %d)", path, MaxFileSizeBytes, info.Size())}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ValidationError{Message: err.Error()}
	}

	// Support both YAML and JSON workflow files.
	var wf *Workflow
	if strings.HasSuffix(path, ".json") {
		wf, err = ParseWorkflowJSON(data)
	} else {
		wf, err = ParseWorkflow(data)
	}
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Dir(path)
	if err := resolveRefs(wf, baseDir); err != nil {
		return nil, err
	}

	wf.BaseDir = baseDir
	return wf, nil
}

// ParseWorkflowJSON unmarshals JSON data into a Workflow and validates it.
// Used for coordinator output (JSON format) and .json workflow files.
// (2026-05-04) - switched from json.Unmarshal to json.Decoder
// with DisallowUnknownFields so that schema.json's
// `additionalProperties: false` is enforced symmetrically with the YAML
// path. Previously YAML rejected unknown fields via yaml.v3
// `KnownFields(true)` but JSON silently dropped them, letting a
// consumer's typo in a `.json` workflow pass without diagnostic.
func ParseWorkflowJSON(data []byte) (*Workflow, error) {
	if len(data) > MaxFileSizeBytes {
		return nil, &ValidationError{Message: "workflow JSON exceeds max file size (§14.2.1)"}
	}
	var wf Workflow
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wf); err != nil {
		return nil, &ValidationError{Message: err.Error()}
	}

	if err := SanitizeWorkflowUnicode(&wf); err != nil {
		return nil, err
	}
	if _, err := ValidateWorkflow(&wf); err != nil {
		return nil, err
	}
	if err := enforceLimits(&wf); err != nil {
		return nil, err
	}

	ApplyDefaults(&wf)

	return &wf, nil
}

// ParseWorkflow unmarshals YAML data into a Workflow and validates it.
// It does NOT resolve @ refs (no baseDir context).
func ParseWorkflow(data []byte) (*Workflow, error) {
	if len(data) > MaxFileSizeBytes {
		return nil, &ValidationError{Message: "workflow YAML exceeds max file size (§14.2.1)"}
	}
	var wf Workflow
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&wf); err != nil {
		return nil, &ValidationError{Message: err.Error()}
	}

	if err := SanitizeWorkflowUnicode(&wf); err != nil {
		return nil, err
	}
	if _, err := ValidateWorkflow(&wf); err != nil {
		return nil, err
	}
	if err := enforceLimits(&wf); err != nil {
		return nil, err
	}

	// Apply schema defaults so callers always see documented values, not empty strings.
	ApplyDefaults(&wf)

	return &wf, nil
}

// SanitizeWorkflowUnicode runs SanitizeUnicode on every user-facing
// string field of wf and validates agent map keys against
// strictStepIDPattern.
// Rationale:
// - Bidi-override code points (U+202A-E, U+2066-9) and other
// non-printable controls in instructions / prompts can be used to
// hide malicious payloads inside an otherwise innocent-looking
// workflow. Reject at parse time so downstream consumers see only
// normalised text.
// - Agent map keys propagate unsanitised into prompt scaffolding,
// trace IDs, and filesystem paths (the include resolver uses agent
// identity in error messages). An attacker-controlled key like
// `../../etc/passwd` or `agent\nrm -rf` could escape any of those
// contexts. The strict pattern rejects everything outside
// `^[a-z][a-z0-9_-]{0,63}$` (matches step IDs).
func SanitizeWorkflowUnicode(wf *Workflow) error {
	clean, err := SanitizeUnicode(wf.Name)
	if err != nil {
		return &ValidationError{Message: "workflow name: " + err.Error()}
	}
	wf.Name = clean
	// Homoglyph-mixed-script signal. Purely observational;
	// legitimate multilingual names exist, so we log rather than
	// reject. A warning here gives operators a place to look first when
	// a workflow exhibits odd LLM behaviour that may trace back to a
	// Latin-looking identifier that actually contains Cyrillic
	// lookalikes.
	if wf.Name != "" && DetectMixedScript(wf.Name) {
		slog.Warn("workflow name contains mixed scripts (possible homoglyph)", "name", wf.Name)
	}

	clean, err = SanitizeUnicode(wf.Description)
	if err != nil {
		return &ValidationError{Message: "workflow description: " + err.Error()}
	}
	wf.Description = clean

	for i := range wf.Steps {
		clean, err = SanitizeUnicode(wf.Steps[i].ID)
		if err != nil {
			return &ValidationError{Message: fmt.Sprintf("step[%d] id: %s", i, err.Error())}
		}
		wf.Steps[i].ID = clean

		clean, err = SanitizeUnicode(wf.Steps[i].Instructions)
		if err != nil {
			return &ValidationError{Message: fmt.Sprintf("step %q instructions: %s", wf.Steps[i].ID, err.Error())}
		}
		wf.Steps[i].Instructions = clean
	}

	if wf.Agents != nil {
 // F13: enforce strict key pattern BEFORE rewriting the map so a
 // rejection error reports the offending key verbatim.
		for key := range wf.Agents {
			if !strictStepIDPattern.MatchString(key) {
				return &ValidationError{Message: fmt.Sprintf("agent name %q must match %s", key, strictStepIDPattern.String())}
			}
		}
 // F14: sanitise prompts. Agent keys already passed strict
 // validation so they need no Unicode pass.
		for name, agent := range wf.Agents {
			clean, err = SanitizeUnicode(agent.Prompt)
			if err != nil {
				return &ValidationError{Message: fmt.Sprintf("agent %q prompt: %s", name, err.Error())}
			}
			agent.Prompt = clean
			wf.Agents[name] = agent
		}
	}
	return nil
}

// ApplyDefaults fills in schema-defined default values for fields left at their
// Go zero value after YAML unmarshalling.
func ApplyDefaults(wf *Workflow) {
	// Schema declares `version` default 1; fill so callers that omit
	// the field see the documented value rather than the Go zero (0).
	if wf.Version == 0 {
		wf.Version = 1
	}
	if wf.Options.OnStepFailure == "" {
		wf.Options.OnStepFailure = spec.FailureCascade
	}
	if wf.Options.Scheduler == "" {
		wf.Options.Scheduler = spec.SchedulerDependencyFirst
	}
}

// ValidateWorkflow checks workflow constraints: name, steps, unique IDs, valid refs, no cycles.
// Returns the topological order on success.
func ValidateWorkflow(wf *Workflow) ([]string, error) {
	// 1. Name check.
	if wf.Name == "" {
		return nil, &MissingNameError{Message: "workflow name is required"}
	}

	// 2. Steps non-empty.
	if len(wf.Steps) == 0 {
		return nil, &NoStepsError{Message: "workflow must have at least one step"}
	}

	// 3. Version check - 0 means absent (Go zero-value), 1 is the only valid explicit version.
	// Note: JSON Schema says minimum:1, but Go cannot distinguish absent from explicit 0.
	// A workflow with explicit `version: 0` will be accepted by Go but rejected by JSON Schema.
	// To fix this, Version would need to be *int - deferred as a low-priority schema alignment task.
	if wf.Version != 0 && wf.Version != 1 {
		return nil, &ValidationError{Message: fmt.Sprintf("unknown version %d (only version 1 is supported)", wf.Version)}
	}

	// 4. Agent validation: description required + range checks.
	// Note: Schema says minimum:1 for maxTurns, but Go treats 0 as unset/default.
	// An explicit `maxTurns: 0` in YAML is accepted and treated as "use default"
	// by the executor. Negative values are rejected.
	// All per-agent violations are independent - accumulate and join.
	agentErrs := make([]error, 0, len(wf.Agents))
	for name, agent := range wf.Agents {
		if agent.Description == "" {
			agentErrs = append(agentErrs, &ValidationError{Message: fmt.Sprintf("agent %q missing required field 'description'", name)})
		}
		if agent.MaxTurns < 0 {
			agentErrs = append(agentErrs, &ValidationError{Message: fmt.Sprintf("agent %q: maxTurns must be non-negative", name)})
		}
		if agent.Temperature != nil && (*agent.Temperature < 0 || *agent.Temperature > 2) {
			agentErrs = append(agentErrs, &ValidationError{Message: fmt.Sprintf("agent %q: temperature must be between 0 and 2", name)})
		}
		if agent.TopP != nil && (*agent.TopP < 0 || *agent.TopP > 1) {
			agentErrs = append(agentErrs, &ValidationError{Message: fmt.Sprintf("agent %q: topP must be between 0 and 1", name)})
		}
	}
	if len(agentErrs) > 0 {
		return nil, errors.Join(agentErrs...)
	}

	// 5. Step ID - empty, pattern, duplicate.
	seen := make(map[string]bool, len(wf.Steps))
	for i, s := range wf.Steps {
		if s.ID == "" {
			return nil, &ValidationError{Message: fmt.Sprintf("step at index %d has empty ID", i)}
		}
		if !stepIDPattern.MatchString(s.ID) {
			return nil, &ValidationError{Message: fmt.Sprintf("step ID %q must match pattern ^[a-zA-Z][a-zA-Z0-9_-]*$", s.ID)}
		}
		if seen[s.ID] {
			return nil, &DuplicateStepError{Message: fmt.Sprintf("duplicate step ID %q", s.ID), StepID: s.ID}
		}
		seen[s.ID] = true
	}

	// 5b. Condition and Loop.Until minLength: 1 (spec rejects empty strings).
	for _, s := range wf.Steps {
		if s.Condition != nil && *s.Condition == "" {
			return nil, &ValidationError{Message: fmt.Sprintf("step %q: condition must not be empty (minLength: 1)", s.ID)}
		}
		if s.Loop != nil && s.Loop.Until != nil && *s.Loop.Until == "" {
			return nil, &ValidationError{Message: fmt.Sprintf("step %q: loop.until must not be empty (minLength: 1)", s.ID)}
		}
	}

	// 6. Include mutual exclusion.
	for _, s := range wf.Steps {
		if s.Include != "" {
			if s.Agent != "" {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and agent", s.ID), StepID: s.ID, Field: "agent"}
			}
			if s.Instructions != "" {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and instructions", s.ID), StepID: s.ID, Field: "instructions"}
			}
			if s.Loop != nil {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and loop", s.ID), StepID: s.ID, Field: "loop"}
			}
			if s.Condition != nil {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and condition", s.ID), StepID: s.ID, Field: "condition"}
			}
			if len(s.ContextFiles) > 0 {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and contextFiles", s.ID), StepID: s.ID, Field: "contextFiles"}
			}
			if s.Model != "" {
				return nil, &IncludeConflictError{Message: fmt.Sprintf("step %q has include and model", s.ID), StepID: s.ID, Field: "model"}
			}
		}
	}

	// 7. Loop validation.
	for _, s := range wf.Steps {
		if s.Loop == nil {
			continue
		}
		l := s.Loop

 // Loop maxConcurrency must be non-negative. 0 is treated as "unset"
 // (forEach: all-parallel; workflow-level: falls through to
 // WithMaxConcurrency > defaultMaxConcurrency=5). Negative values are
 // rejected. Schema enforces minimum:0; this rejects negatives that
 // JSON-Schema validators would otherwise pass through with type
 // coercion.
		if l.MaxConcurrency < 0 {
			return nil, &ValidationError{Message: fmt.Sprintf("step %q: loop maxConcurrency must be non-negative", s.ID)}
		}

 // loop.steps minItems:1 - if steps array is explicitly present but empty, reject.
		if l.Steps != nil && len(l.Steps) == 0 {
			return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: loop.steps must have at least one step if present", s.ID), StepID: s.ID}
		}

		hasForEach := l.ForEach != nil

		if hasForEach {
 // forEach must be string (CEL expression) or []any (static array).
			switch l.ForEach.(type) {
			case string, []any:
 // valid types
			default:
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach must be string or array, got %T", s.ID, l.ForEach), StepID: s.ID}
			}

 // forEach mode - mutually exclusive with repeat-until fields.
			if l.MaxIterations != nil {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach is mutually exclusive with maxIterations", s.ID), StepID: s.ID}
			}
			if l.Until != nil {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach is mutually exclusive with until", s.ID), StepID: s.ID}
			}
			if l.UntilAgent != "" {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach is mutually exclusive with untilAgent", s.ID), StepID: s.ID}
			}
			if l.Delay.D() > 0 {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach is mutually exclusive with delay", s.ID), StepID: s.ID}
			}
 // forEach empty array check.
			if arr, ok := l.ForEach.([]any); ok && len(arr) == 0 {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: forEach array is empty", s.ID), StepID: s.ID}
			}
		} else {
 // until and untilAgent may both be present (spec §6: until is evaluated first,
 // then untilAgent if until is false). No mutual exclusion.

 // repeat-until mode - maxIterations required (nil means not set).
			if l.MaxIterations == nil {
				return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: repeat-until loop requires maxIterations > 0", s.ID), StepID: s.ID}
			}
 // Negative/zero maxIterations - explicit values <=0 are invalid.
			if *l.MaxIterations <= 0 {
				return nil, &ValidationError{Message: fmt.Sprintf("step %q: maxIterations must be positive", s.ID)}
			}
		}

 // Validate inner steps: nested loop prohibition, step IDs, agent refs, dep refs, include conflicts.
		if err := validateInnerSteps(wf, s.ID, l.Steps); err != nil {
			return nil, err
		}

 // outputMode validation: empty (= "last") or one of the named constants.
		switch l.OutputMode {
		case "", spec.LoopOutputModeLast, spec.LoopOutputModeCumulative:
 // valid
		default:
			return nil, &LoopValidationError{Message: fmt.Sprintf("step %q: invalid loop.outputMode %q (must be %q or %q)", s.ID, l.OutputMode, spec.LoopOutputModeLast, spec.LoopOutputModeCumulative), StepID: s.ID}
		}

 // untilAgent reference validation.
		if l.UntilAgent != "" {
			if wf.Agents == nil {
				return nil, &ValidationError{Message: fmt.Sprintf("step %q: untilAgent references unknown agent %q (no agents defined)", s.ID, l.UntilAgent)}
			}
			agent, ok := wf.Agents[l.UntilAgent]
			if !ok {
				return nil, &ValidationError{Message: fmt.Sprintf("step %q: untilAgent references unknown agent %q", s.ID, l.UntilAgent)}
			}
 // Spec requires untilAgent agent to have resultSchema with done boolean in required.
			if agent.ResultSchema == nil {
				return nil, &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q must have resultSchema", s.ID, l.UntilAgent)}
			}
			if err := validateUntilAgentSchema(s.ID, l.UntilAgent, agent.ResultSchema); err != nil {
				return nil, err
			}
		}
	}

	// 8. Negative values.
	if wf.Options.MaxConcurrency < 0 {
		return nil, &ValidationError{Message: "maxConcurrency must be non-negative"}
	}

	// 9. Agent references - also when agents map is nil. Step retries range check.
	// All per-step violations are independent - accumulate and join.
	stepErrs := make([]error, 0, len(wf.Steps))
	for _, s := range wf.Steps {
		if s.Agent != "" {
			if wf.Agents == nil {
				stepErrs = append(stepErrs, &MissingAgentError{Message: s.Agent + " (referenced by step " + s.ID + ", no agents defined)", Agent: s.Agent, StepID: s.ID})
			} else if _, ok := wf.Agents[s.Agent]; !ok {
				stepErrs = append(stepErrs, &MissingAgentError{Message: s.Agent + " (referenced by step " + s.ID + ")", Agent: s.Agent, StepID: s.ID})
			}
		}
		if s.Retries < 0 {
			stepErrs = append(stepErrs, &ValidationError{Message: fmt.Sprintf("step %q: retries must be non-negative", s.ID)})
		}
		if s.MaxRetries != nil && *s.MaxRetries < 0 {
			stepErrs = append(stepErrs, &ValidationError{Message: fmt.Sprintf("step %q: maxRetries must be non-negative", s.ID)})
		}
	}
	if wf.Options.MaxRetries != nil && *wf.Options.MaxRetries < 0 {
		stepErrs = append(stepErrs, &ValidationError{Message: "options.maxRetries must be non-negative"})
	}
	if len(stepErrs) > 0 {
		return nil, errors.Join(stepErrs...)
	}

	// 10. Dependency references - accumulate all missing-dep violations.
	depErrs := make([]error, 0, len(wf.Steps))
	for _, s := range wf.Steps {
		for _, dep := range s.DependsOn {
			if !seen[dep] {
				depErrs = append(depErrs, &MissingDepError{Message: dep + " (referenced by step " + s.ID + ")", Dep: dep, StepID: s.ID})
			}
		}
	}
	if len(depErrs) > 0 {
		return nil, errors.Join(depErrs...)
	}

	// 11. Validate onStepFailure if set.
	if s := wf.Options.OnStepFailure; s != "" {
		switch s {
		case spec.FailureCascade, spec.FailureSkipDependents, spec.FailureAbort:
 // valid
		default:
			return nil, &ValidationError{Message: fmt.Sprintf("invalid onStepFailure %q (must be cascade, skip-dependents, or abort)", s)}
		}
	}

	// 12. Validate scheduling strategy if set.
	if s := wf.Options.Scheduler; s != "" {
		switch s {
		case spec.SchedulerDependencyFirst, spec.SchedulerRoundRobin, spec.SchedulerLeastBusy:
 // valid
		default:
			return nil, &ValidationError{Message: fmt.Sprintf("invalid scheduling %q (must be dependency-first, round-robin, or least-busy)", s)}
		}
	}

	// 13. Check for cycles and get topological order.
	order, err := TopoSort(wf.Steps)
	if err != nil {
		return nil, err
	}

	return order, nil
}

// resolveRefs replaces @ prefixed strings with file contents. Depth is
// tracked through chained references (an included file whose own first
// non-empty content begins with `@` triggers another resolution) and
// rejected at depth > MaxNestingDepth.
func resolveRefs(wf *Workflow, baseDir string) error {
	for name, agent := range wf.Agents {
		if strings.HasPrefix(agent.Prompt, "@") {
			content, err := resolveChainedRef(baseDir, agent.Prompt[1:], 1, map[string]bool{})
			if err != nil {
				return &ValidationError{Message: "agent " + name + " prompt: " + err.Error()}
			}
			agent.Prompt = content
			wf.Agents[name] = agent
		}
	}

	for i := range wf.Steps {
		if strings.HasPrefix(wf.Steps[i].Instructions, "@") {
			content, err := resolveChainedRef(baseDir, wf.Steps[i].Instructions[1:], 1, map[string]bool{})
			if err != nil {
				return &ValidationError{Message: "step " + wf.Steps[i].ID + " instructions: " + err.Error()}
			}
			wf.Steps[i].Instructions = content
		}
	}

	return nil
}

// resolveChainedRef expands a single `@` reference and, if the loaded
// content itself begins with another `@` token, recurses with a
// depth-bounded counter. depth starts at 1 for the top-level reference.
// MaxNestingDepth (20) is intentionally larger than the executor-side
// MaxIncludeDepth (5) - the parser cannot know whether a chain will
// terminate without expanding it, so we accept a deeper bound at parse
// time and let the runtime executor reject pathological include graphs
// it discovers later. Going past 20 still strongly suggests an attack
// or accidental cycle (F12).
func resolveChainedRef(baseDir, relPath string, depth int, visited map[string]bool) (string, error) {
	if depth > MaxNestingDepth {
		return "", fmt.Errorf("reference nesting depth %d exceeds max %d", depth, MaxNestingDepth)
	}
	// R7A-15: cycle detection via abs-path visited set. Without this, a
	// pair of files that @-reference each other (or a self-reference)
	// would silently consume the depth budget every traversal until
	// MaxNestingDepth fires - report the cycle directly instead.
	absKey, err := filepathAbs(filepath.Join(baseDir, relPath))
	if err != nil {
		return "", fmt.Errorf("resolve ref path: %w", err)
	}
	if visited[absKey] {
		return "", &ValidationError{Message: fmt.Sprintf("@-ref cycle detected at %q (depth %d)", relPath, depth)}
	}
	visited[absKey] = true
	content, err := readRef(baseDir, relPath)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "@") {
 // The inclusion target is itself another @-ref. Recurse so we
 // land on the terminal text content, with depth tracking.
		next := strings.TrimSpace(trimmed[1:])
 // Reject empty refs (`@` followed by whitespace) as malformed
 // rather than silently infinite-looping on `readRef`.
		if next == "" {
			return "", fmt.Errorf("empty chained reference at depth %d", depth)
		}
		return resolveChainedRef(baseDir, next, depth+1, visited)
	}
	return content, nil
}

func readRef(baseDir, relPath string) (string, error) {
	fullPath := filepath.Join(baseDir, relPath)
	// Prevent path traversal: resolved path must stay within baseDir.
	absResolved, err := filepathAbs(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	absBase, err := filepathAbs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base: %w", err)
	}
	// Resolve symlinks to prevent symlink-based traversal bypass.
	if realResolved, err := filepath.EvalSymlinks(absResolved); err == nil {
		absResolved = realResolved
	}
	if realBase, err := filepath.EvalSymlinks(absBase); err == nil {
		absBase = realBase
	}
	if !strings.HasPrefix(absResolved, absBase+string(filepath.Separator)) && absResolved != absBase {
		return "", fmt.Errorf("path %q: %w", relPath, ErrRefPathEscape)
	}
	// Read from the resolved path (not the original fullPath) to prevent TOCTOU
	// race where a symlink could be swapped between the check and the read.
	// R7A-3: stat-before-read so a multi-GB hostile @-referenced file is
	// rejected without first being slurped into memory. On Stat failure we
	// surface the error rather than fall through to ReadFile - otherwise a
	// transient stat error would silently bypass the size cap.
	info, statErr := os.Stat(absResolved)
	if statErr != nil {
		return "", fmt.Errorf("stat ref %q: %w", relPath, statErr)
	}
	if info.Size() > MaxFileSizeBytes {
		return "", fmt.Errorf("ref %q exceeds %d bytes (got %d)", relPath, MaxFileSizeBytes, info.Size())
	}
	data, err := os.ReadFile(absResolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// validateInnerSteps validates loop inner steps: nested loop prohibition,
// step ID pattern/duplicates, agent refs, dep refs, include conflicts.
func validateInnerSteps(wf *Workflow, parentID string, steps []Step) error {
	// Pass 1: Collect all inner step IDs (check empty, pattern, duplicates, nested loop, include).
	innerSeen := make(map[string]bool, len(steps))
	for _, inner := range steps {
		if inner.Loop != nil {
			return &LoopValidationError{Message: fmt.Sprintf("step %q: inner step %q has nested loop (prohibited)", parentID, inner.ID), StepID: parentID}
		}
		if inner.ID == "" {
			return &ValidationError{Message: fmt.Sprintf("step %q: inner step has empty ID", parentID)}
		}
		if !stepIDPattern.MatchString(inner.ID) {
			return &ValidationError{Message: fmt.Sprintf("step %q: inner step ID %q must match pattern ^[a-zA-Z][a-zA-Z0-9_-]*$", parentID, inner.ID)}
		}
		if innerSeen[inner.ID] {
			return &DuplicateStepError{Message: fmt.Sprintf("step %q: duplicate inner step ID %q", parentID, inner.ID), StepID: inner.ID}
		}
		innerSeen[inner.ID] = true
 // Agent reference check.
		if inner.Agent != "" {
			if wf.Agents == nil {
				return &MissingAgentError{Message: inner.Agent + " (referenced by inner step " + inner.ID + " in " + parentID + ", no agents defined)", Agent: inner.Agent, StepID: inner.ID}
			}
			if _, ok := wf.Agents[inner.Agent]; !ok {
				return &MissingAgentError{Message: inner.Agent + " (referenced by inner step " + inner.ID + " in " + parentID + ")", Agent: inner.Agent, StepID: inner.ID}
			}
		}
 // Include mutual exclusion.
		if inner.Include != "" {
			if inner.Agent != "" {
				return &IncludeConflictError{Message: fmt.Sprintf("inner step %q in %q has include and agent", inner.ID, parentID), StepID: inner.ID, Field: "agent"}
			}
			if inner.Instructions != "" {
				return &IncludeConflictError{Message: fmt.Sprintf("inner step %q in %q has include and instructions", inner.ID, parentID), StepID: inner.ID, Field: "instructions"}
			}
			if inner.Condition != nil {
				return &IncludeConflictError{Message: fmt.Sprintf("inner step %q in %q has include and condition", inner.ID, parentID), StepID: inner.ID, Field: "condition"}
			}
			if len(inner.ContextFiles) > 0 {
				return &IncludeConflictError{Message: fmt.Sprintf("inner step %q in %q has include and contextFiles", inner.ID, parentID), StepID: inner.ID, Field: "contextFiles"}
			}
			if inner.Model != "" {
				return &IncludeConflictError{Message: fmt.Sprintf("inner step %q in %q has include and model", inner.ID, parentID), StepID: inner.ID, Field: "model"}
			}
		}
	}

	// Pass 2: Validate dependency references (all IDs now in innerSeen).
	for _, inner := range steps {
		for _, dep := range inner.DependsOn {
			if !innerSeen[dep] {
				return &MissingDepError{Message: dep + " (referenced by inner step " + inner.ID + " in " + parentID + ")", Dep: dep, StepID: inner.ID}
			}
		}
	}

	// Pass 3: Cycle detection for inner step sub-DAG.
	if _, err := TopoSort(steps); err != nil {
		return err
	}

	return nil
}

// validateUntilAgentSchema checks that an untilAgent's resultSchema has
// properties.done (boolean) in required, per spec.
func validateUntilAgentSchema(stepID, agentName string, schema map[string]any) error {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema must have properties", stepID, agentName)}
	}
	doneProp, ok := props["done"]
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema must have properties.done", stepID, agentName)}
	}
	doneDef, ok := doneProp.(map[string]any)
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema.properties.done must be an object", stepID, agentName)}
	}
	if doneType, _ := doneDef["type"].(string); doneType != "boolean" {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema.properties.done must have type boolean", stepID, agentName)}
	}
	// Check done is in required array.
	req, ok := schema["required"]
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema must have 'done' in required array", stepID, agentName)}
	}
	reqSlice, ok := toAnySlice(req)
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema.required must be an array", stepID, agentName)}
	}
	for _, r := range reqSlice {
		if s, ok := r.(string); ok && s == "done" {
			return nil
		}
	}
	return &ValidationError{Message: fmt.Sprintf("step %q: untilAgent %q resultSchema must have 'done' in required array", stepID, agentName)}
}

func toAnySlice(v any) ([]any, bool) {
	switch arr := v.(type) {
	case []any:
		return arr, true
	case []string:
		result := make([]any, len(arr))
		for i, s := range arr {
			result[i] = s
		}
		return result, true
	}
	return nil, false
}
