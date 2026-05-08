package exec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zendev-sh/goai/provider"

	"github.com/zendev-sh/zenflow/internal/spec"
)

// ForEachContext holds per-iteration context for forEach loops.
type ForEachContext struct {
	Item  any // The current forEach item.
	Index int // Zero-based index of the current iteration.
}

// Context-window budget constants for. These cap the prompt size to
// prevent context-window overflow on smaller models (DeepSeek V3, MiniMax,
// Codex) when workflows accumulate many step results.
// The values are conservative defaults tuned against the smallest models
// we target (~64k-token context window, ~4 chars/token ⇒ ~256k chars raw,
// minus system prompt, tool schemas, and output reservation). The limits
// can be overridden per-workflow in the future if needed.
const (
	// maxPromptBytes caps the total assembled prompt size. 120 KB ≈ 30k
	// tokens, leaving comfortable headroom for system prompt + tool schemas
	// + output on a 64k-token model, and ample headroom on larger models.
	maxPromptBytes = 120 * 1024

	// maxDepContentBytes caps each individual dependency's Content+Result
	// injection. Prevents one huge dep from monopolizing the budget; also
	// keeps prompts manageable when a workflow has many small deps.
	maxDepContentBytes = 16 * 1024

	// truncationMarker is appended to any truncated section so the LLM
	// understands the content was elided intentionally.
	truncationMarker = "\n...[truncated for context limit]\n"
)

// MaxAttachmentSizeBytes moved to limits.go (centralized with other Max* caps).

// truncateForContext shortens s to at most max bytes, appending the
// truncation marker when truncation occurs. Safe for unicode byte input -
// it trims on a byte boundary and the marker is ASCII so the result stays
// valid UTF-8 as long as the input was valid UTF-8 (common case: LLM text
// output, which is UTF-8).
func truncateForContext(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	// Leave room for the marker.
	keep := maxLen - len(truncationMarker)
	if keep < 0 {
		keep = 0
	}
	// Trim to a safe byte boundary by walking back to the last valid
	// rune start. Go's string indexing by byte is fine; this just avoids
	// cutting a multibyte rune in half.
	for keep > 0 && (s[keep]&0xC0) == 0x80 {
		keep--
	}
	return s[:keep] + truncationMarker
}

// AssemblePrompt builds the user prompt from agent config, step definition,
// workflow base directory (for resolving relative context file paths), and
// completed results from dependency steps.
// Returns the text prompt and any multimodal attachments (images, PDFs).
func AssemblePrompt(agent AgentConfig, step Step, baseDir string, priorResults map[string]*StepResult) (string, []provider.Part) {
	return AssemblePromptWithForEach(agent, step, baseDir, priorResults, nil)
}

// AssemblePromptWithForEach builds the user prompt with optional forEach item injection.
// Returns the text prompt and any multimodal attachments (images, PDFs).
func AssemblePromptWithForEach(agent AgentConfig, step Step, baseDir string, priorResults map[string]*StepResult, fe *ForEachContext) (string, []provider.Part) {
	var sb strings.Builder
	// follow-up: if ANY dep has PreserveContent
	// set, skip the overall maxPromptBytes truncation pass too. Caller
	// signaled intentional large aggregation (e.g. cumulative loop) and
	// accepts the prompt blowing the byte budget. Without this, the
	// overall cap fires at 120KB and truncates from the END (which is
	// where the latest iteration's data lives) - exactly the
	// "truncated at end of con-argue" symptom the user reported.
	preserveOverall := false
	for _, sr := range priorResults {
		if sr != nil && sr.PreserveContent {
			preserveOverall = true
			break
		}
	}

	// : agent.Prompt is now passed to goai.WithSystem (see
	// executor_step.go runStep). The legacy "## Agent Role" header
	// in the user message has been retired so agent identity lives
	// in the system slot, where it is architecturally correct (LLM
	// providers treat system + user prompts differently for instruction
	// following + safety filters; agent role is identity, not task).
	if step.Instructions != "" {
 // : agent.Prompt previously preceded "## Task" so a
 // leading "\n" separator was needed. With the prompt now in
 // the system slot, "## Task" is always the first section in
 // the user message - no separator required.
		sb.WriteString("## Task\n")
		sb.WriteString(step.Instructions)
		sb.WriteString("\n")
	}

	// When the agent has a resultSchema, instruct it to call submit_result.
	if agent.ResultSchema != nil {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("## Required: Submit Structured Result\n")
		sb.WriteString("You MUST call the `submit_result` tool with your final answer. ")
		sb.WriteString("Do NOT just return text - you must use the submit_result tool to submit a JSON object matching the required schema. ")
		sb.WriteString("The conversation will end when you call submit_result.\n")
		if schemaJSON, err := json.Marshal(agent.ResultSchema); err == nil {
			sb.WriteString("Required schema: ")
			sb.Write(schemaJSON)
			sb.WriteString("\n")
		}
	}

	// Inject completed dependency outputs.
	// each dep's content+result is capped at maxDepContentBytes to
	// prevent any single huge dep from blowing the context window.
	if len(step.DependsOn) > 0 && len(priorResults) > 0 {
		var hasResults bool
		for _, depID := range step.DependsOn {
			if sr, ok := priorResults[depID]; ok && sr != nil && sr.Status == spec.StepCompleted {
				if !hasResults {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString("## Previous Step Results\n")
					hasResults = true
				}
				writeDepSection(&sb, depID, sr)
			}
		}
	} else if len(step.DependsOn) == 0 && len(priorResults) > 0 {
 // G3: Parent dep results from include step (spec §7 dependsOn rewriting).
 // Inner steps with no dependsOn receive parent context via ParentDepResults.
		var hasResults bool
		for depID, sr := range priorResults {
			if sr == nil || sr.Status != spec.StepCompleted {
				continue
			}
			if !hasResults {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("## Parent Context\n")
				hasResults = true
			}
			writeDepSection(&sb, depID, sr)
		}
	}

	var attachments []provider.Part
	if len(step.ContextFiles) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("## Context Files\n")
		for _, f := range step.ContextFiles {
 // Resolve relative paths against the workflow's base directory.
			path := f
			if baseDir != "" && !filepath.IsAbs(f) {
				path = filepath.Join(baseDir, f)
			}
 // Path traversal + symlink check: resolved path must stay within baseDir.
			if baseDir != "" {
				absResolved, err := filepathAbs(path)
				if err != nil {
					sb.WriteString("### ")
					sb.WriteString(f)
					sb.WriteString("\n(error: cannot resolve path)\n")
					continue
				}
				absBase, err := filepathAbs(baseDir)
				if err != nil {
					sb.WriteString("### ")
					sb.WriteString(f)
					sb.WriteString("\n(error: cannot resolve base directory)\n")
					continue
				}
				if realResolved, err := filepath.EvalSymlinks(absResolved); err == nil {
					absResolved = realResolved
				}
				if realBase, err := filepath.EvalSymlinks(absBase); err == nil {
					absBase = realBase
				}
				if !strings.HasPrefix(absResolved, absBase+string(filepath.Separator)) && absResolved != absBase {
					sb.WriteString("### ")
					sb.WriteString(f)
					sb.WriteString("\n(error: path escapes workflow directory)\n")
					continue
				}
 // Use the resolved path to prevent TOCTOU race.
				path = absResolved
			}

 // Detect file type by extension.
			ext := strings.ToLower(filepath.Ext(f))
			mediaType := mediaTypeForExt(ext)

			if mediaType != "" {
 // R7A-4: stat-before-read so a multi-GB hostile attachment
 // is rejected without first allocating the full payload.
 // On Stat failure, skip with an embedded error rather than
 // falling through to ReadFile (which would bypass the size
 // cap if Stat fails transiently but ReadFile then succeeds).
				info, statErr := os.Stat(path)
				if statErr != nil {
					sb.WriteString("### ")
					sb.WriteString(f)
					sb.WriteString("\n(error reading file: ")
					sb.WriteString(statErr.Error())
					sb.WriteString(")\n")
					continue
				}
				if info.Size() > MaxAttachmentSizeBytes {
					sb.WriteString("### ")
					sb.WriteString(f)
					fmt.Fprintf(&sb, "\n(error: file exceeds %d MiB attachment cap)\n", MaxAttachmentSizeBytes/1024/1024)
					continue
				}
 // Binary attachment (image or PDF) → multimodal part.
				data, err := os.ReadFile(path)
				if err != nil {
					sb.WriteString("### ")
					sb.WriteString(f)
					sb.WriteString("\n(error reading file: ")
					sb.WriteString(err.Error())
					sb.WriteString(")\n")
					continue
				}
				dataURI := "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
				if isImageMediaType(mediaType) {
					attachments = append(attachments, provider.Part{
						Type: provider.PartImage,
						URL:  dataURI,
					})
				} else {
 // PDF/document → PartFile with data URI.
					attachments = append(attachments, provider.Part{
						Type:      provider.PartFile,
						URL:       dataURI,
						MediaType: mediaType,
						Filename:  filepath.Base(f),
					})
				}
				sb.WriteString("### ")
				sb.WriteString(f)
				sb.WriteString("\n(attached as multimodal content)\n")
			} else {
 // Text file → inline in prompt.
				sb.WriteString("### ")
				sb.WriteString(f)
				sb.WriteString("\n")
 // R7A-4: stat-before-read; on Stat failure embed the error and
 // skip rather than fall through to ReadFile (which would
 // bypass the size cap if Stat fails transiently).
				info, statErr := os.Stat(path)
				if statErr != nil {
					sb.WriteString("(error reading file: ")
					sb.WriteString(statErr.Error())
					sb.WriteString(")\n")
					continue
				}
				if info.Size() > MaxAttachmentSizeBytes {
					fmt.Fprintf(&sb, "(error: file exceeds %d MiB attachment cap)\n", MaxAttachmentSizeBytes/1024/1024)
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					sb.WriteString("(error reading file: ")
					sb.WriteString(err.Error())
					sb.WriteString(")\n")
				} else {
					sb.Write(data)
					if len(data) > 0 && data[len(data)-1] != '\n' {
						sb.WriteString("\n")
					}
				}
			}
		}
	}

	// forEach item injection: inject current item and index into prompt.
	if fe != nil {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "## forEach Item (index: %d)\n", fe.Index)
		itemJSON, err := json.Marshal(fe.Item)
		if err != nil {
			fmt.Fprintf(&sb, "%v", fe.Item)
		} else {
			sb.Write(itemJSON)
		}
		sb.WriteString("\n")
	}

	// final global cap on the assembled prompt. Per-dep truncation
	// above handles the common case, but a pathological workflow (hundreds
	// of deps, huge context files, or massive forEach item) could still
	// accumulate over the budget. Cap at maxPromptBytes as a safety net.
	// follow-up: when any dep opted in to PreserveContent, skip
	// the overall cap too.
	out := sb.String()
	if !preserveOverall && len(out) > maxPromptBytes {
		out = truncateForContext(out, maxPromptBytes)
	}
	return out, attachments
}

// mediaTypeForExt returns the MIME type for binary file extensions.
// Returns empty string for text files (which are inlined in the prompt).
func mediaTypeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

// isImageMediaType returns true for image MIME types.
func isImageMediaType(mt string) bool {
	return strings.HasPrefix(mt, "image/")
}

// writeDepSection writes one dependency's completed StepResult to sb with
// per-dep byte budget enforcement. Long dep content is truncated
// with a clear marker so the LLM knows content was elided. The result map
// is serialized as JSON and also capped.
func writeDepSection(sb *strings.Builder, depID string, sr *StepResult) {
	sb.WriteString("### ")
	sb.WriteString(depID)
	sb.WriteString(" (completed)\n")
	// respect StepResult.PreserveContent - set by loop steps in
	// outputMode=cumulative to signal that the content was intentionally
	// aggregated and must reach the dependent step intact. The overall
	// maxPromptBytes cap still applies via the final AssemblePrompt
	// truncation pass at line ~265, so this can't blow the context budget.
	content := sr.Content
	if !sr.PreserveContent {
		content = truncateForContext(content, maxDepContentBytes)
	}
	sb.WriteString(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		sb.WriteString("\n")
	}
	if sr.Result != nil {
		sb.WriteString("### ")
		sb.WriteString(depID)
		sb.WriteString(" result\n")
		resultJSON, _ := json.Marshal(sr.Result)
		resultStr := string(resultJSON)
		if !sr.PreserveContent {
			resultStr = truncateForContext(resultStr, maxDepContentBytes)
		}
		sb.WriteString(resultStr)
		sb.WriteString("\n")
	}
}
