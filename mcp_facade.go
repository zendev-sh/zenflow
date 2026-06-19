package zenflow

// mcp_facade.go - root re-exports for the native MCP (Model Context Protocol)
// surface implemented in internal/exec/mcp.go. Embedders load a Claude-
// compatible settings.json, connect to the declared servers, and layer the
// discovered tools onto the orchestrator with WithAdditionalTools:
//
//	cfg, err := zenflow.LoadMCPConfig(".zenflow/settings.json")
//	if err != nil { /* errors.Is(err, os.ErrNotExist) => no MCP configured */ }
//	ts, err := zenflow.ConnectMCPConfig(ctx, cfg)
//	defer ts.Close()
//	orch := zenflow.New(
//	    zenflow.WithModel(llm),
//	    zenflow.WithTools(builtins...),
//	    zenflow.WithAdditionalTools(ts.Tools()...),
//	)

import "github.com/zendev-sh/zenflow/internal/exec"

// MCP config + result types re-exported from internal/exec.
type (
	// MCPServerConfig describes one MCP server (stdio or remote).
	MCPServerConfig = exec.MCPServerConfig
	// MCPConfig is the parsed settings.json document.
	MCPConfig = exec.MCPConfig
	// MCPToolset owns live MCP clients and exposes their tools.
	MCPToolset = exec.MCPToolset
	// MCPOption configures ConnectMCPConfig.
	MCPOption = exec.MCPOption
)

// MCP functions + options re-exported from internal/exec.
var (
	// LoadMCPConfig reads and parses an MCP settings file.
	LoadMCPConfig = exec.LoadMCPConfig
	// ConnectMCPConfig connects to every server in a config and aggregates tools.
	ConnectMCPConfig = exec.ConnectMCPConfig
	// WithMCPClientInfo sets the advertised client name/version.
	WithMCPClientInfo = exec.WithMCPClientInfo
	// WithMCPRequestTimeout bounds each MCP request.
	WithMCPRequestTimeout = exec.WithMCPRequestTimeout
	// WithMCPStderr routes stdio subprocess stderr.
	WithMCPStderr = exec.WithMCPStderr
)
