package exec

import "regexp"

// prefixPatterns match "prefix + secret" pairs. The first capture group is
// the non-secret prefix (e.g. "Authorization: Bearer ") which is preserved
// in the output; everything after the group is replaced with [REDACTED].
var prefixPatterns = []*regexp.Regexp{
	// Authorization: Bearer <token>
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)\S+`),
	// X-Api-Key: <value>
	regexp.MustCompile(`(?i)(x-api-key:\s*)\S+`),
	// --password <value> / --password=<value>
	regexp.MustCompile(`(?i)(--password[=\s]+)\S+`),
	// api_key / api-key / apikey = "value" or :"value"
	regexp.MustCompile(`(?i)(api[_-]?key["']?\s*[:=]\s*["']?)[\w-]+`),
}

// bareTokenPatterns match entire well-known secret tokens with no leading
// non-secret prefix. The whole match is replaced with [REDACTED].
var bareTokenPatterns = []*regexp.Regexp{
	// OpenAI sk-, GitHub ghp_, Slack xox[bp]-, AWS AKIA
	regexp.MustCompile(`\b(?:sk-[a-zA-Z0-9]{20,}|ghp_[a-zA-Z0-9]{20,}|xox[bp]-[a-zA-Z0-9-]+|AKIA[A-Z0-9]{16})\b`),
}

// redactSecrets replaces known secret patterns in input with [REDACTED].
// It is intentionally conservative - it may miss novel secret formats but
// will not corrupt unrelated text. Prefix patterns preserve the matched
// header/flag name so log entries remain readable.
func redactSecrets(input string) string {
	out := input
	for _, p := range prefixPatterns {
		out = p.ReplaceAllString(out, "${1}[REDACTED]")
	}
	for _, p := range bareTokenPatterns {
		out = p.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
