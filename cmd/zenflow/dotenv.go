package main

// dotenv.go - optional .env auto-loading for the flow / goal / agent
// subcommands. Before a run, zenflow reads a shell-style .env file
// (default ./.env in the working directory) and sets every variable it
// declares that is NOT already present in the process environment - an
// exported value always wins over the file. The populated environment then
// feeds ${VAR} interpolation in workflow YAML (issue #16) and ${VAR}
// expansion in MCP server config.
//
// Like .zenflow/settings.json, a .env is a trust boundary: it can inject
// secrets and paths into the run, so it is loaded only from the working
// directory. Disable it with --no-dotenv or point at an explicit file with
// --env-file.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// defaultDotEnvPath is the conventional env file, relative to the working
// directory (after --workdir has chdir'd).
const defaultDotEnvPath = ".env"

// osSetenv is a test seam for the process-environment mutation.
var osSetenv = os.Setenv

// dotEnvEntry is a single parsed KEY=VALUE pair.
type dotEnvEntry struct{ key, val string }

// loadDotEnv reads the .env file selected by flags and applies its entries
// to the process environment without overriding already-set variables. A
// missing default file is silently ignored; a missing explicit --env-file
// is reported. Parse/read errors are reported and skipped rather than
// aborting the run, mirroring the best-effort posture of MCP loading.
func loadDotEnv(flags cmdFlags) {
	if flags.noDotEnv {
		return
	}
	path := flags.envFile
	explicit := path != ""
	if path == "" {
		path = defaultDotEnvPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if explicit {
				fmt.Fprintf(stderr, "zenflow: env file not found: %s\n", path)
			}
			return
		}
		fmt.Fprintf(stderr, "zenflow: env file: %v\n", err)
		return
	}
	entries, perr := parseDotEnv(string(data))
	if perr != nil {
		fmt.Fprintf(stderr, "zenflow: env file %s: %v\n", path, perr)
		return
	}
	set := 0
	for _, e := range entries {
		if _, ok := os.LookupEnv(e.key); ok {
			continue // exported value wins over .env
		}
		if err := osSetenv(e.key, e.val); err != nil {
			fmt.Fprintf(stderr, "zenflow: env file %s: set %s: %v\n", path, e.key, err)
			continue
		}
		set++
	}
	if flags.verbose && set > 0 {
		fmt.Fprintf(stderr, "zenflow: loaded %d var(s) from %s\n", set, path)
	}
}

// parseDotEnv parses KEY=VALUE lines. It supports blank lines, `#`
// comments, an optional leading `export `, single/double-quoted values
// (surrounding quotes stripped, contents kept verbatim) and unquoted
// values (surrounding whitespace trimmed, a trailing ` #` inline comment
// dropped). It rejects lines without `=` and invalid variable names so a
// typo fails fast rather than silently doing nothing.
func parseDotEnv(s string) ([]dotEnvEntry, error) {
	var out []dotEnvEntry
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '=' (expected KEY=VALUE)", lineNo)
		}
		key := strings.TrimSpace(k)
		if !isEnvKey(key) {
			return nil, fmt.Errorf("line %d: invalid variable name %q", lineNo, key)
		}
		out = append(out, dotEnvEntry{key: key, val: unquoteDotEnvValue(strings.TrimSpace(v))})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// unquoteDotEnvValue strips a matching pair of surrounding quotes, or for
// an unquoted value drops a trailing ` #` inline comment.
func unquoteDotEnvValue(v string) string {
	if len(v) >= 2 {
		if c := v[0]; (c == '"' || c == '\'') && v[len(v)-1] == c {
			return v[1 : len(v)-1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// isEnvKey reports whether k is a valid env var name ([A-Za-z_][A-Za-z0-9_]*).
func isEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		alpha := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		digit := c >= '0' && c <= '9'
		switch {
		case i == 0 && alpha:
			// leading char must be a letter or underscore
		case i > 0 && (alpha || digit):
			// subsequent chars may also be digits
		default:
			return false
		}
	}
	return true
}
