package exec

// mcp.go - native Model Context Protocol (MCP) support. zenflow reads a
// Claude-compatible settings.json describing one or more MCP servers,
// connects to each via goai's MCP client, and exposes their tools as
// ordinary goai.Tool values. No tool-loop reimplementation lives here -
// goai owns the wire protocol (mcp.Client) and the tool adapter
// (mcp.ConvertTools); this file is the config -> clients -> tools glue
// plus deterministic namespacing so multiple servers cannot collide.
//
// Discovered tools are namespaced "<server>__<tool>" so an agent's tool
// allowlist can grant a whole server by its bare name (see FilterTools)
// while individual tools stay addressable. The Execute closure built by
// mcp.ConvertTools captures the server-side tool name, so renaming the
// goai.Tool.Name for namespacing does not affect dispatch.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/mcp"
)

// MCPServerConfig describes a single MCP server entry. The shape matches the
// Claude Desktop "mcpServers" convention so existing config
// files port over unchanged. A stdio server sets Command (+ optional Args /
// Env); a remote server sets URL (+ optional Headers). Type selects the
// transport explicitly; when empty it is inferred (Command -> stdio,
// URL -> http). String values in Command, Args, Env, URL, and Headers are
// expanded with os.ExpandEnv, so "${FIRECRAWL_API_KEY}" resolves from the
// process environment.
type MCPServerConfig struct {
	// Command is the executable for a stdio (local subprocess) server.
	Command string `json:"command,omitempty"`
	// Args are the arguments passed to Command.
	Args []string `json:"args,omitempty"`
	// Env sets additional environment variables for the subprocess. Merged
	// onto the parent process environment.
	Env map[string]string `json:"env,omitempty"`
	// URL is the endpoint for a remote (http / sse) server.
	URL string `json:"url,omitempty"`
	// Headers are sent on every request to a remote server (e.g. auth).
	Headers map[string]string `json:"headers,omitempty"`
	// Type forces the transport: "stdio", "http", or "sse". Empty infers
	// from Command / URL.
	Type string `json:"type,omitempty"`
	// Disabled skips this server entirely when true.
	Disabled bool `json:"disabled,omitempty"`
}

// MCPConfig is the top-level settings.json document: a map of server name to
// its configuration under the "mcpServers" key.
type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// LoadMCPConfig reads and parses an MCP settings file. A missing file
// returns an error satisfying errors.Is(err, os.ErrNotExist) so callers can
// treat "no config" as a no-op without a separate stat. A present but
// malformed file returns a parse error.
func LoadMCPConfig(path string) (*MCPConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// %w preserves os.ErrNotExist for the documented errors.Is contract
		// while keeping the package prefix on other read failures.
		return nil, fmt.Errorf("zenflow: read MCP config %s: %w", path, err)
	}
	var cfg MCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("zenflow: parse MCP config %s: %w", path, err)
	}
	return &cfg, nil
}

// MCPToolset is the live result of connecting to one or more MCP servers. It
// owns the underlying clients (and, for stdio servers, their subprocesses);
// callers MUST invoke Close to release them. Tools returns the aggregated,
// namespaced goai.Tool slice ready for WithAdditionalTools.
type MCPToolset struct {
	tools   []goai.Tool
	clients []*mcp.Client
	servers []string
}

// Tools returns the discovered tools across all connected servers, each
// named "<server>__<tool>". The slice is freshly allocated; mutating it does
// not affect the toolset.
func (t *MCPToolset) Tools() []goai.Tool {
	if t == nil {
		return nil
	}
	out := make([]goai.Tool, len(t.tools))
	copy(out, t.tools)
	return out
}

// Servers returns the names of the servers that connected successfully.
func (t *MCPToolset) Servers() []string {
	if t == nil {
		return nil
	}
	out := make([]string, len(t.servers))
	copy(out, t.servers)
	return out
}

// Close shuts down every connected client, terminating stdio subprocesses.
// Safe to call once; errors from individual closes are joined.
func (t *MCPToolset) Close() error {
	if t == nil {
		return nil
	}
	var errs []error
	for _, c := range t.clients {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// mcpConnectConfig holds the resolved options for ConnectMCPConfig.
type mcpConnectConfig struct {
	clientName     string
	clientVersion  string
	requestTimeout time.Duration
	stderr         io.Writer
	// transportFactory is an unexported test seam. When nil, the default
	// stdio/http/sse factory derived from MCPServerConfig is used.
	transportFactory func(name string, sc MCPServerConfig) (mcp.Transport, error)
}

// MCPOption configures ConnectMCPConfig.
type MCPOption func(*mcpConnectConfig)

// WithMCPClientInfo sets the client name/version advertised to MCP servers
// during the initialize handshake. Defaults to "zenflow"/"1.0.0".
func WithMCPClientInfo(name, version string) MCPOption {
	return func(c *mcpConnectConfig) {
		if name != "" {
			c.clientName = name
		}
		if version != "" {
			c.clientVersion = version
		}
	}
}

// WithMCPRequestTimeout bounds each MCP request, including the connect
// handshake and every subsequent tool call. Zero leaves goai's default
// (60s). Note: for stdio servers this does NOT bound the subprocess
// lifetime - that is governed by the context passed to ConnectMCPConfig and
// by MCPToolset.Close.
func WithMCPRequestTimeout(d time.Duration) MCPOption {
	return func(c *mcpConnectConfig) { c.requestTimeout = d }
}

// WithMCPStderr routes the stderr of stdio MCP subprocesses to w (e.g. for
// verbose diagnostics). Default discards it.
func WithMCPStderr(w io.Writer) MCPOption {
	return func(c *mcpConnectConfig) { c.stderr = w }
}

// withMCPTransportFactory injects a transport factory (test seam).
func withMCPTransportFactory(f func(name string, sc MCPServerConfig) (mcp.Transport, error)) MCPOption {
	return func(c *mcpConnectConfig) { c.transportFactory = f }
}

// ConnectMCPConfig connects to every (enabled) server in cfg and returns a
// toolset aggregating their tools. Servers are processed in deterministic
// (sorted) order. A failure connecting to one server does not abort the
// others: the returned toolset contains the tools from all servers that
// connected, and the returned error is the join of per-server failures
// (nil when all succeed). Callers should use the toolset even on a non-nil
// error and always Close it.
//
// For stdio servers, ctx governs the subprocess lifetime (goai starts them
// with exec.CommandContext): pass a context that stays valid for as long as
// the tools may be invoked, and rely on MCPToolset.Close - not context
// cancellation - for orderly shutdown. A short-lived/timeout context would
// kill the subprocess immediately after the handshake.
func ConnectMCPConfig(ctx context.Context, cfg *MCPConfig, opts ...MCPOption) (*MCPToolset, error) {
	cc := mcpConnectConfig{clientName: "zenflow", clientVersion: "1.0.0"}
	for _, o := range opts {
		o(&cc)
	}

	ts := &MCPToolset{}
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return ts, nil
	}

	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		sc := cfg.MCPServers[name]
		if sc.Disabled {
			continue
		}
		// Reject names containing the namespace separator: they would make
		// "<server>__<tool>" prefixes ambiguous between distinct servers, so
		// an agent's bare-server-name grant could no longer be resolved
		// unambiguously.
		if strings.Contains(name, mcpNameSep) {
			errs = append(errs, fmt.Errorf("mcp server %q: name must not contain %q", name, mcpNameSep))
			continue
		}
		tools, client, err := cc.connectOne(ctx, name, sc)
		if err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", name, err))
			continue
		}
		ts.clients = append(ts.clients, client)
		ts.servers = append(ts.servers, name)
		ts.tools = append(ts.tools, tools...)
	}
	return ts, errors.Join(errs...)
}

// connectOne connects to a single server and returns its namespaced tools.
func (cc mcpConnectConfig) connectOne(ctx context.Context, name string, sc MCPServerConfig) ([]goai.Tool, *mcp.Client, error) {
	factory := cc.transportFactory
	if factory == nil {
		factory = func(n string, s MCPServerConfig) (mcp.Transport, error) {
			return defaultMCPTransport(n, s, cc.stderr)
		}
	}
	transport, err := factory(name, sc)
	if err != nil {
		return nil, nil, err
	}

	clientOpts := []mcp.ClientOption{mcp.WithTransport(transport)}
	if cc.requestTimeout > 0 {
		clientOpts = append(clientOpts, mcp.WithRequestTimeout(cc.requestTimeout))
	}
	client := mcp.NewClient(cc.clientName, cc.clientVersion, clientOpts...)

	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		return nil, nil, err
	}

	mcpTools, err := listAllMCPTools(ctx, client)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}

	tools := mcp.ConvertTools(client, mcpTools)
	prefix := name + mcpNameSep
	for i := range tools {
		tools[i].Name = prefix + tools[i].Name
	}
	return tools, client, nil
}

// listAllMCPTools collects a server's full tool list, following pagination.
func listAllMCPTools(ctx context.Context, client *mcp.Client) ([]mcp.Tool, error) {
	res, err := client.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	all := res.Tools
	for res.NextCursor != "" {
		cursor := res.NextCursor
		res, err = client.ListTools(ctx, &mcp.ListParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		// Guard against a server that returns the same cursor forever (an
		// unbounded loop on untrusted input). A well-behaved server either
		// advances the cursor or clears it to end pagination.
		if res.NextCursor == cursor {
			return nil, fmt.Errorf("mcp: tools/list cursor did not advance (stuck at %q)", cursor)
		}
	}
	return all, nil
}

// defaultMCPTransport builds the goai transport for a server config. stderr,
// when non-nil, captures a stdio subprocess's stderr stream.
func defaultMCPTransport(name string, sc MCPServerConfig, stderr io.Writer) (mcp.Transport, error) {
	typ := sc.Type
	if typ == "" {
		switch {
		case sc.Command != "":
			typ = "stdio"
		case sc.URL != "":
			typ = "http"
		default:
			return nil, fmt.Errorf("config has neither command (stdio) nor url (http/sse)")
		}
	}

	switch typ {
	case "stdio":
		if sc.Command == "" {
			return nil, fmt.Errorf("stdio transport requires a command")
		}
		var stdioOpts []mcp.StdioOption
		if len(sc.Env) > 0 {
			stdioOpts = append(stdioOpts, mcp.WithStdioEnv(expandEnvMap(sc.Env)))
		}
		if stderr != nil {
			stdioOpts = append(stdioOpts, mcp.WithStdioStderr(stderr))
		}
		return mcp.NewStdioTransport(os.ExpandEnv(sc.Command), expandEnvSlice(sc.Args), stdioOpts...), nil
	case "http":
		if sc.URL == "" {
			return nil, fmt.Errorf("http transport requires a url")
		}
		var httpOpts []mcp.HTTPTransportOption
		if len(sc.Headers) > 0 {
			httpOpts = append(httpOpts, mcp.WithHTTPHeaders(expandEnvMap(sc.Headers)))
		}
		return mcp.NewHTTPTransport(os.ExpandEnv(sc.URL), httpOpts...), nil
	case "sse":
		if sc.URL == "" {
			return nil, fmt.Errorf("sse transport requires a url")
		}
		var sseOpts []mcp.SSETransportOption
		if len(sc.Headers) > 0 {
			sseOpts = append(sseOpts, mcp.WithSSEHeaders(expandEnvMap(sc.Headers)))
		}
		return mcp.NewSSETransport(os.ExpandEnv(sc.URL), sseOpts...), nil
	default:
		return nil, fmt.Errorf("unknown transport type %q (want stdio, http, or sse)", typ)
	}
}

// expandEnvSlice applies os.ExpandEnv to each element.
func expandEnvSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = os.ExpandEnv(s)
	}
	return out
}

// expandEnvMap applies os.ExpandEnv to each value.
func expandEnvMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = os.ExpandEnv(v)
	}
	return out
}

// mcpNameSep is the separator between an MCP server name and its tool name in
// the namespaced tool identifier. Built-in and auto-injected tool names never
// contain it, so server grouping (FilterTools / ValidateToolNames) cannot
// false-match a non-MCP tool.
const mcpNameSep = "__"

// mcpGroupMatches reports whether entry selects toolName as an MCP server
// group: entry "firecrawl" matches every tool named "firecrawl__<tool>".
func mcpGroupMatches(toolName, entry string) bool {
	return strings.HasPrefix(toolName, entry+mcpNameSep)
}
