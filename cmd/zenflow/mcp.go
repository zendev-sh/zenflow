package main

// mcp.go - CLI glue for native MCP support. zenflow reads a Claude-
// compatible settings.json (default .zenflow/settings.json), connects to
// every declared server, and layers the discovered tools onto the
// orchestrator via WithAdditionalTools. The returned cleanup closes the
// servers (terminating stdio subprocesses); callers defer it.
//
// A present `.zenflow/settings.json` is loaded automatically (like an editor
// loading project config). It is a trust boundary - a stdio server runs its
// `command` locally - so only run zenflow in directories you trust; use
// --no-mcp to skip loading or --mcp-config to point at a file you control.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/zenflow"
)

// defaultMCPConfigPath is the conventional location of the MCP settings
// file, relative to the working directory. Matches the path users reach for
// by analogy with other agent tools' project config.
const defaultMCPConfigPath = ".zenflow/settings.json"

// mcpResult is the outcome of resolving + connecting MCP servers.
type mcpResult struct {
	tools   []goai.Tool
	servers []string // connected server names, for logging
	cleanup func()   // caller MUST defer this
	active  bool     // true when at least one tool was discovered
}

// mcpInactive is the "nothing loaded" result with a no-op cleanup.
var mcpInactive = mcpResult{cleanup: func() {}}

// mcpLoad resolves the MCP config and connects to its servers. It is a seam:
// tests swap it to exercise loadMCPOptions without real subprocesses.
var mcpLoad = connectMCPServers

// loadMCPOptions turns the discovered MCP tools into orchestrator option(s)
// plus a cleanup func the caller MUST defer. When MCP is disabled, absent,
// or empty it returns no options and a no-op cleanup.
func loadMCPOptions(flags cmdFlags) ([]zenflow.Option, func()) {
	r := mcpLoad(flags)
	if !r.active {
		return nil, r.cleanup
	}
	if flags.verbose {
		fmt.Fprintf(stderr, "zenflow: MCP loaded %d tool(s) from server(s) %v\n", len(r.tools), r.servers)
	}
	return []zenflow.Option{zenflow.WithAdditionalTools(r.tools...)}, r.cleanup
}

// connectMCPServers is the production implementation of mcpLoad.
//
// The connection uses context.Background() deliberately: for stdio servers
// goai ties the subprocess lifetime to this context, so it must outlive the
// run. Orderly shutdown happens through the returned cleanup (Toolset.Close),
// not context cancellation. The connect handshake is bounded by goai's
// per-request timeout (60s), so a hung server cannot block indefinitely.
func connectMCPServers(flags cmdFlags) mcpResult {
	if flags.noMCP {
		return mcpInactive
	}

	path := flags.mcpConfig
	explicit := path != ""
	if path == "" {
		path = defaultMCPConfigPath
	}

	cfg, err := zenflow.LoadMCPConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Only complain when the user explicitly pointed us at a path.
			if explicit {
				fmt.Fprintf(stderr, "zenflow: MCP config not found: %s\n", path)
			}
			return mcpInactive
		}
		fmt.Fprintf(stderr, "zenflow: MCP config: %v\n", err)
		return mcpInactive
	}

	warnInsecureMCP(cfg)

	mcpOpts := []zenflow.MCPOption{}
	if flags.verbose {
		// Surface the subprocess stderr (npx download chatter, server logs)
		// only in verbose mode to keep normal runs quiet.
		mcpOpts = append(mcpOpts, zenflow.WithMCPStderr(stderr))
	}

	ts, err := zenflow.ConnectMCPConfig(context.Background(), cfg, mcpOpts...)
	if err != nil {
		// Partial success is possible: report the failures, keep going with
		// whatever connected.
		fmt.Fprintf(stderr, "zenflow: MCP: %v\n", err)
	}

	tools := ts.Tools()
	if len(tools) == 0 {
		_ = ts.Close()
		return mcpInactive
	}
	return mcpResult{tools: tools, servers: ts.Servers(), cleanup: func() { _ = ts.Close() }, active: true}
}

// warnInsecureMCP flags remote servers that would send headers (e.g. a bearer
// token) over plaintext http://.
func warnInsecureMCP(cfg *zenflow.MCPConfig) {
	for name, s := range cfg.MCPServers {
		if s.Disabled {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.URL)), "http://") && len(s.Headers) > 0 {
			fmt.Fprintf(stderr, "zenflow: warning: MCP server %q sends headers over plaintext http:// (use https://)\n", name)
		}
	}
}
