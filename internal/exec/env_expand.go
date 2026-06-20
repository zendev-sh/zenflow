package exec

// env_expand.go - ${VAR} / $VAR environment-variable interpolation for
// workflow free-text fields (issue #16). The operator's process
// environment (populated via `export` or an auto-loaded .env at the CLI
// layer) is the trust boundary: references resolve from os.Getenv and an
// unset variable expands to the empty string, matching shell `export`
// semantics so a partially-configured flow degrades the same way a shell
// script would rather than erroring out.
//
// Scope is deliberately narrow. Only natural-language fields are expanded:
// the workflow description, agent prompts, and step instructions
// (including steps nested in loops). Structural identifiers (names, IDs)
// and machine-interpreted fields are left alone - in particular toolInput
// already reserves a leading `$` for CEL expressions (spec §7.4), so
// expanding it here would collide with that contract.

import (
	"os"
	"strings"
)

// expandEnvVars substitutes ${NAME} and $NAME with the value of the NAME
// environment variable, or the empty string when NAME is unset. NAME must
// match [A-Za-z_][A-Za-z0-9_]*. A literal dollar is written `$$` (`$${X}`
// yields the literal text `${X}`), mirroring the `$$` -> `$` escape the
// spec already uses for toolInput CEL values. Any other `$` - `$5`, `$ `,
// a trailing `$`, or `${` without a valid name/close - is left untouched,
// so prices and shell snippets embedded in free text survive verbatim.
func expandEnvVars(s string) string {
	start := strings.IndexByte(s, '$')
	if start < 0 {
		return s // fast path: nothing to expand
	}
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString(s[:start])
	for i := start; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// s[i] == '$'
		if i+1 >= len(s) {
			b.WriteByte('$') // trailing '$' is literal
			break
		}
		switch next := s[i+1]; {
		case next == '$':
			b.WriteByte('$') // `$$` escape -> literal `$`
			i += 2
		case next == '{':
			closeIdx := strings.IndexByte(s[i+2:], '}')
			name := ""
			if closeIdx >= 0 {
				name = s[i+2 : i+2+closeIdx]
			}
			if closeIdx >= 0 && isEnvName(name) {
				b.WriteString(os.Getenv(name))
				i = i + 2 + closeIdx + 1
			} else {
				// Unterminated or non-name `${...}`: emit `$` and rescan.
				b.WriteByte('$')
				i++
			}
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			b.WriteString(os.Getenv(s[i+1 : j]))
			i = j
		default:
			b.WriteByte('$') // `$5`, `$ `, etc. stay literal
			i++
		}
	}
	return b.String()
}

// isEnvName reports whether name is a valid environment-variable
// identifier ([A-Za-z_][A-Za-z0-9_]*).
func isEnvName(name string) bool {
	if name == "" || !isNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isNameChar(name[i]) {
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// expandWorkflowEnv interpolates environment variables across every
// free-text field of wf in place: the description, each agent prompt, and
// every step's instructions (recursing into loop bodies).
func expandWorkflowEnv(wf *Workflow) {
	wf.Description = expandEnvVars(wf.Description)
	for name, agent := range wf.Agents {
		agent.Prompt = expandEnvVars(agent.Prompt)
		wf.Agents[name] = agent
	}
	expandStepsEnv(wf.Steps)
}

// expandStepsEnv expands instructions for steps and any nested loop steps.
func expandStepsEnv(steps []Step) {
	for i := range steps {
		steps[i].Instructions = expandEnvVars(steps[i].Instructions)
		if steps[i].Loop != nil {
			expandStepsEnv(steps[i].Loop.Steps)
		}
	}
}
