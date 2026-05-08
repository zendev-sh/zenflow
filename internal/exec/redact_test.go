package exec

import "testing"

// TestRedactSecrets verifies each pattern used by redactSecrets.
func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
 // No trailing quote - \S+ stops at space.
			name:  "authorization bearer token",
			input: `curl -H Authorization: Bearer sk-abcdefghij1234567890XYZ --verbose`,
			want:  `curl -H Authorization: Bearer [REDACTED] --verbose`,
		},
		{
			name:  "authorization bearer token case-insensitive",
			input: `authorization: bearer ghp_ABCDEFGHIJKLMNOPQRSTuvwxyz01`,
			want:  `authorization: bearer [REDACTED]`,
		},
		{
			name:  "x-api-key header",
			input: `X-Api-Key: supersecretvalue`,
			want:  `X-Api-Key: [REDACTED]`,
		},
		{
			name:  "x-api-key lowercase",
			input: `x-api-key: another-secret-123`,
			want:  `x-api-key: [REDACTED]`,
		},
		{
			name:  "password long flag",
			input: `--password=s3cr3tP@ssw0rd`,
			want:  `--password=[REDACTED]`,
		},
		{
			name:  "password space flag",
			input: `--password mypassword`,
			want:  `--password [REDACTED]`,
		},
		{
			name:  "api_key assignment",
			input: `{"api_key": "my-key-value"}`,
			want:  `{"api_key": "[REDACTED]"}`,
		},
		{
			name:  "api-key colon",
			input: `api-key:"abcdef"`,
			want:  `api-key:"[REDACTED]"`,
		},
		{
 // Bare sk- token (20+ chars after prefix). Surrounded by word boundaries.
			name:  "openai sk- prefix token",
			input: `sk-abcdefghijklmnopqrstuvwxyz0123456789`,
			want:  `[REDACTED]`,
		},
		{
 // Bare ghp_ token (20+ chars after prefix).
			name:  "github ghp_ prefix token",
			input: `ghp_ABCDEFGHIJKLMNOPQRSTuvwxyz0123456789`,
			want:  `[REDACTED]`,
		},
		{
 // Bare xoxb- Slack token.
			name:  "slack xoxb- prefix token",
			input: `xoxb-1234567890-abcdefghijklmno`,
			want:  `[REDACTED]`,
		},
		{
 // Bare AKIA AWS access key (exactly 20 chars).
			name:  "aws akia prefix token",
			input: `AKIAIOSFODNN7EXAMPLE`,
			want:  `[REDACTED]`,
		},
		{
			name:  "no secrets - plain text unchanged",
			input: `echo hello world`,
			want:  `echo hello world`,
		},
		{
			name:  "empty string unchanged",
			input: ``,
			want:  ``,
		},
		{
 // Multiple patterns in one string.
			name:  "multiple secrets in one string",
			input: `Authorization: Bearer mytoken X-Api-Key: mykey`,
			want:  `Authorization: Bearer [REDACTED] X-Api-Key: [REDACTED]`,
		},
		{
 // sk- token with only 19 chars after prefix - below the 20-char threshold, not redacted.
			name:  "sk- token too short - not redacted",
			input: `sk-tooshortXXXXXXXXX`,
			want:  `sk-tooshortXXXXXXXXX`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.input)
			if got != tc.want {
				t.Errorf("redactSecrets(%q)\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}
}
