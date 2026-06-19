package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/mcp"
)

// fakeMCPTransport is an in-process mcp.Transport that answers the MCP
// handshake plus tools/list (with optional pagination) and tools/call from
// canned data. It lets us exercise ConnectMCPConfig end to end without a real
// subprocess.
type fakeMCPTransport struct {
	tools         []mcp.Tool
	pageSize      int    // 0 => single page
	startErr      error  // returned from Start
	listErr       bool   // tools/list replies with a JSON-RPC error
	listErrOnCall int32  // if >0, the Nth tools/list call errors (1-based)
	stuckCursor   bool   // always return the same non-empty cursor (never ends)
	closeErr      error  // returned from Close
	callReply     string // text content returned from tools/call
	noTools       bool   // advertise no tools capability (assertCapability fails)

	listCalls atomic.Int32
	onMessage func(mcp.JSONRPCMessage)
	closed    bool
}

func (f *fakeMCPTransport) Start(_ context.Context) error         { return f.startErr }
func (f *fakeMCPTransport) OnMessage(fn func(mcp.JSONRPCMessage)) { f.onMessage = fn }
func (f *fakeMCPTransport) OnClose(func())                        {}
func (f *fakeMCPTransport) OnError(func(error))                   {}
func (f *fakeMCPTransport) Close() error                          { f.closed = true; return f.closeErr }

// mcpToolNames extracts the names from a goai.Tool slice for assertions.
func mcpToolNames(tools []goai.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func (f *fakeMCPTransport) reply(id any, result any) {
	raw, _ := json.Marshal(result)
	f.onMessage(mcp.JSONRPCMessage{JSONRPC: "2.0", ID: id, Result: raw})
}

func (f *fakeMCPTransport) replyError(id any, code int, msg string) {
	f.onMessage(mcp.JSONRPCMessage{JSONRPC: "2.0", ID: id, RPCError: &mcp.JSONRPCError{Code: code, Message: msg}})
}

func (f *fakeMCPTransport) Send(_ context.Context, msg mcp.JSONRPCMessage) error {
	go func() {
		switch msg.Method {
		case "initialize":
			caps := mcp.ServerCapabilities{}
			if !f.noTools {
				caps.Tools = &mcp.ToolsCapability{}
			}
			f.reply(msg.ID, mcp.InitializeResult{
				ProtocolVersion: mcp.ProtocolVersion20250326,
				Capabilities:    caps,
				ServerInfo:      mcp.ServerInfo{Name: "fake", Version: "1.0"},
			})
		case "notifications/initialized":
			// notification: no response
		case "tools/list":
			call := f.listCalls.Add(1)
			if f.listErr || (f.listErrOnCall > 0 && call == f.listErrOnCall) {
				f.replyError(msg.ID, -32000, "list failed")
				return
			}
			start := 0
			if len(msg.Params) > 0 {
				var p mcp.ListParams
				_ = json.Unmarshal(msg.Params, &p)
				if p.Cursor != "" {
					start, _ = strconv.Atoi(p.Cursor)
				}
			}
			if f.stuckCursor {
				f.reply(msg.ID, mcp.ListToolsResult{Tools: f.tools, NextCursor: "stuck"})
				return
			}
			end := len(f.tools)
			next := ""
			if f.pageSize > 0 && start+f.pageSize < len(f.tools) {
				end = start + f.pageSize
				next = strconv.Itoa(end)
			}
			f.reply(msg.ID, mcp.ListToolsResult{Tools: f.tools[start:end], NextCursor: next})
		case "tools/call":
			block, _ := json.Marshal(mcp.TextContent{Type: "text", Text: f.callReply})
			f.reply(msg.ID, mcp.CallToolResult{Content: []mcp.ContentBlock{block}})
		}
	}()
	return nil
}

func mcpTool(name string) mcp.Tool {
	return mcp.Tool{Name: name, Description: "desc", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func fakeFactory(transports map[string]*fakeMCPTransport) func(string, MCPServerConfig) (mcp.Transport, error) {
	return func(name string, _ MCPServerConfig) (mcp.Transport, error) {
		t, ok := transports[name]
		if !ok {
			return nil, errors.New("no fake transport for " + name)
		}
		return t, nil
	}
}

func TestConnectMCPConfig_NamespacesAndAggregates(t *testing.T) {
	ft := map[string]*fakeMCPTransport{
		"alpha": {tools: []mcp.Tool{mcpTool("scrape"), mcpTool("crawl")}, callReply: "alpha-ok"},
		"beta":  {tools: []mcp.Tool{mcpTool("scrape")}, callReply: "beta-ok"},
	}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{
		"alpha": {Command: "x"},
		"beta":  {Command: "y"},
	}}

	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err != nil {
		t.Fatalf("ConnectMCPConfig: %v", err)
	}
	defer ts.Close()

	got := mcpToolNames(ts.Tools())
	want := []string{"alpha__scrape", "alpha__crawl", "beta__scrape"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	if servers := ts.Servers(); strings.Join(servers, ",") != "alpha,beta" {
		t.Fatalf("servers = %v, want [alpha beta]", servers)
	}

	// The namespaced tool dispatches to the server-side name "scrape" and
	// returns the fake's canned content.
	out, err := ts.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "alpha-ok" {
		t.Errorf("Execute result = %q, want alpha-ok", out)
	}
}

func TestConnectMCPConfig_Pagination(t *testing.T) {
	ft := map[string]*fakeMCPTransport{
		"srv": {tools: []mcp.Tool{mcpTool("a"), mcpTool("b"), mcpTool("c"), mcpTool("d"), mcpTool("e")}, pageSize: 2},
	}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"srv": {Command: "x"}}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err != nil {
		t.Fatalf("ConnectMCPConfig: %v", err)
	}
	defer ts.Close()
	if got := len(ts.Tools()); got != 5 {
		t.Fatalf("aggregated %d tools across pages, want 5", got)
	}
}

func TestConnectMCPConfig_DisabledSkipped(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"on": {tools: []mcp.Tool{mcpTool("t")}}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{
		"on":  {Command: "x"},
		"off": {Command: "y", Disabled: true},
	}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err != nil {
		t.Fatalf("ConnectMCPConfig: %v", err)
	}
	defer ts.Close()
	if got := ts.Servers(); len(got) != 1 || got[0] != "on" {
		t.Fatalf("servers = %v, want [on]", got)
	}
}

func TestConnectMCPConfig_PartialFailureJoined(t *testing.T) {
	ft := map[string]*fakeMCPTransport{
		"good": {tools: []mcp.Tool{mcpTool("t")}},
		"bad":  {listErr: true},
	}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{
		"good": {Command: "x"},
		"bad":  {Command: "y"},
	}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err == nil {
		t.Fatal("expected joined error from bad server")
	}
	if !strings.Contains(err.Error(), `mcp server "bad"`) {
		t.Errorf("error = %v, want mention of bad server", err)
	}
	defer ts.Close()
	// good server still loaded.
	if got := ts.Servers(); len(got) != 1 || got[0] != "good" {
		t.Fatalf("servers = %v, want [good]", got)
	}
}

func TestConnectMCPConfig_TransportFactoryError(t *testing.T) {
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	ts, err := ConnectMCPConfig(context.Background(), cfg,
		withMCPTransportFactory(func(string, MCPServerConfig) (mcp.Transport, error) {
			return nil, errors.New("boom")
		}))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(ts.Tools()) != 0 {
		t.Errorf("expected no tools on factory failure")
	}
}

func TestConnectMCPConfig_NilAndEmpty(t *testing.T) {
	for _, cfg := range []*MCPConfig{nil, {}, {MCPServers: map[string]MCPServerConfig{}}} {
		ts, err := ConnectMCPConfig(context.Background(), cfg)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(ts.Tools()) != 0 || len(ts.Servers()) != 0 {
			t.Errorf("expected empty toolset")
		}
		if err := ts.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
}

func TestConnectMCPConfig_ConnectError(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"x": {startErr: errors.New("spawn failed")}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	_, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err == nil || !strings.Contains(err.Error(), "spawn failed") {
		t.Fatalf("err = %v, want spawn failed", err)
	}
}

func TestMCPToolset_CloseClosesClients(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"x": {tools: []mcp.Tool{mcpTool("t")}}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	ts, _ := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err := ts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !ft["x"].closed {
		t.Error("transport not closed")
	}
}

func TestMCPToolset_NilReceiver(t *testing.T) {
	var ts *MCPToolset
	if ts.Tools() != nil || ts.Servers() != nil || ts.Close() != nil {
		t.Error("nil receiver methods should be safe no-ops")
	}
}

func TestConnectMCPConfig_ClientInfoAndStderrOptions(t *testing.T) {
	// Exercise the option setters and the requestTimeout client-option path.
	ft := map[string]*fakeMCPTransport{"x": {tools: []mcp.Tool{mcpTool("t")}}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	var sb strings.Builder
	ts, err := ConnectMCPConfig(context.Background(), cfg,
		WithMCPClientInfo("custom", "9.9"),
		WithMCPClientInfo("", ""), // empty args ignored
		WithMCPStderr(&sb),
		WithMCPRequestTimeout(0), // zero leaves default
		withMCPTransportFactory(fakeFactory(ft)),
	)
	if err != nil {
		t.Fatalf("ConnectMCPConfig: %v", err)
	}
	ts.Close()
	if len(ts.Tools()) != 1 {
		t.Fatalf("tools = %d, want 1", len(ts.Tools()))
	}
}

func TestDefaultMCPTransport(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "secret")
	tests := []struct {
		name    string
		sc      MCPServerConfig
		wantErr string
	}{
		{"stdio inferred", MCPServerConfig{Command: "npx", Args: []string{"-y", "${MCP_TEST_TOKEN}"}, Env: map[string]string{"K": "${MCP_TEST_TOKEN}"}}, ""},
		{"stdio explicit stderr", MCPServerConfig{Type: "stdio", Command: "x"}, ""},
		{"http inferred", MCPServerConfig{URL: "http://localhost", Headers: map[string]string{"A": "${MCP_TEST_TOKEN}"}}, ""},
		{"http explicit", MCPServerConfig{Type: "http", URL: "http://x"}, ""},
		{"sse", MCPServerConfig{Type: "sse", URL: "http://x", Headers: map[string]string{"A": "b"}}, ""},
		{"empty config", MCPServerConfig{}, "neither command"},
		{"stdio no command", MCPServerConfig{Type: "stdio"}, "requires a command"},
		{"http no url", MCPServerConfig{Type: "http"}, "requires a url"},
		{"sse no url", MCPServerConfig{Type: "sse"}, "requires a url"},
		{"unknown type", MCPServerConfig{Type: "carrier-pigeon", Command: "x"}, "unknown transport type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sb strings.Builder
			tr, err := defaultMCPTransport(tt.name, tt.sc, &sb)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tr == nil {
				t.Fatal("nil transport")
			}
		})
	}
}

func TestExpandEnvHelpers(t *testing.T) {
	t.Setenv("ZF_X", "val")
	if got := expandEnvSlice(nil); got != nil {
		t.Errorf("expandEnvSlice(nil) = %v", got)
	}
	if got := expandEnvSlice([]string{"a", "${ZF_X}"}); got[1] != "val" {
		t.Errorf("expandEnvSlice = %v", got)
	}
	if got := expandEnvMap(nil); got != nil {
		t.Errorf("expandEnvMap(nil) = %v", got)
	}
	if got := expandEnvMap(map[string]string{"k": "${ZF_X}"}); got["k"] != "val" {
		t.Errorf("expandEnvMap = %v", got)
	}
}

func TestLoadMCPConfig(t *testing.T) {
	dir := t.TempDir()

	// Valid.
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"mcpServers":{"fs":{"command":"npx","args":["-y","x"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadMCPConfig(good)
	if err != nil {
		t.Fatalf("LoadMCPConfig: %v", err)
	}
	if cfg.MCPServers["fs"].Command != "npx" {
		t.Errorf("parsed command = %q", cfg.MCPServers["fs"].Command)
	}

	// Missing -> os.ErrNotExist.
	_, err = LoadMCPConfig(filepath.Join(dir, "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file err = %v, want ErrNotExist", err)
	}

	// Malformed.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{not json`), 0o600)
	_, err = LoadMCPConfig(bad)
	if err == nil || !strings.Contains(err.Error(), "parse MCP config") {
		t.Errorf("malformed err = %v", err)
	}
}

func TestConnectMCPConfig_PaginationErrorPropagates(t *testing.T) {
	// First page OK with a NextCursor, second page errors.
	ft := map[string]*fakeMCPTransport{
		"srv": {tools: []mcp.Tool{mcpTool("a"), mcpTool("b"), mcpTool("c")}, pageSize: 1, listErrOnCall: 2},
	}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"srv": {Command: "x"}}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("err = %v, want list failed", err)
	}
	if len(ts.Tools()) != 0 {
		t.Errorf("partial pagination must not yield tools")
	}
}

func TestConnectMCPConfig_DefaultFactoryRealSpawnFails(t *testing.T) {
	// No injected factory: ConnectMCPConfig builds a real stdio transport via
	// defaultMCPTransport and tries to spawn the command, which does not exist.
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{
		"ghost": {Command: "zenflow-no-such-mcp-binary-xyz"},
	}}
	ts, err := ConnectMCPConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected connect error for nonexistent command")
	}
	if !strings.Contains(err.Error(), `mcp server "ghost"`) {
		t.Errorf("err = %v, want mention of ghost server", err)
	}
	if len(ts.Tools()) != 0 {
		t.Error("expected no tools")
	}
}

func TestConnectMCPConfig_RequestTimeoutOption(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"x": {tools: []mcp.Tool{mcpTool("t")}}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	ts, err := ConnectMCPConfig(context.Background(), cfg,
		WithMCPRequestTimeout(5*time.Second),
		withMCPTransportFactory(fakeFactory(ft)))
	if err != nil {
		t.Fatalf("ConnectMCPConfig: %v", err)
	}
	defer ts.Close()
	if len(ts.Tools()) != 1 {
		t.Fatalf("tools = %d, want 1", len(ts.Tools()))
	}
}

func TestMCPToolset_CloseJoinsErrors(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"x": {tools: []mcp.Tool{mcpTool("t")}, closeErr: errors.New("close boom")}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"x": {Command: "c"}}}
	ts, _ := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err := ts.Close(); err == nil || !strings.Contains(err.Error(), "close boom") {
		t.Fatalf("Close err = %v, want close boom", err)
	}
}

func TestWithAdditionalTools_Appends(t *testing.T) {
	o := &Orchestrator{}
	WithTools(makeTool("read", "", "ok"))(o)
	WithAdditionalTools(makeTool("firecrawl__scrape", "", "ok"), makeTool("firecrawl__crawl", "", "ok"))(o)
	got := mcpToolNames(o.tools)
	want := []string{"read", "firecrawl__scrape", "firecrawl__crawl"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestFilterTools_MCPServerGroup(t *testing.T) {
	catalog := []goai.Tool{
		makeTool("read", "", "ok"),
		makeTool("firecrawl__scrape", "", "ok"),
		makeTool("firecrawl__crawl", "", "ok"),
		makeTool("github__search", "", "ok"),
	}
	// Bare server name grants every tool of that server, plus an exact name.
	got := mcpToolNames(FilterTools(catalog, []string{"read", "firecrawl"}, nil))
	want := []string{"read", "firecrawl__scrape", "firecrawl__crawl"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allow group = %v, want %v", got, want)
	}
	// Disallow a server group.
	got = mcpToolNames(FilterTools(catalog, nil, []string{"firecrawl"}))
	want = []string{"read", "github__search"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("disallow group = %v, want %v", got, want)
	}
}

func TestValidateToolNames_MCPServerGroup(t *testing.T) {
	catalog := []goai.Tool{
		makeTool("read", "", "ok"),
		makeTool("firecrawl__scrape", "", "ok"),
	}
	// Bare server group "firecrawl" is valid because a namespaced tool exists.
	okWF := &Workflow{Agents: map[string]AgentConfig{
		"a": {Tools: []string{"read", "firecrawl"}},
	}}
	if err := ValidateToolNames(okWF, catalog); err != nil {
		t.Errorf("expected group reference to validate, got %v", err)
	}
	// An unknown server group is rejected.
	badWF := &Workflow{Agents: map[string]AgentConfig{
		"a": {Tools: []string{"slack"}},
	}}
	if err := ValidateToolNames(badWF, catalog); err == nil {
		t.Error("expected unknown group to fail validation")
	}
}

func TestConnectMCPConfig_RejectsServerNameWithSeparator(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"good": {tools: []mcp.Tool{mcpTool("t")}}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{
		"good": {Command: "x"},
		"a__b": {Command: "y"}, // name contains the namespace separator
	}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err == nil || !strings.Contains(err.Error(), `must not contain "__"`) {
		t.Fatalf("err = %v, want rejection of \"a__b\"", err)
	}
	defer ts.Close()
	// The valid server still loads.
	if got := ts.Servers(); len(got) != 1 || got[0] != "good" {
		t.Fatalf("servers = %v, want [good]", got)
	}
}

func TestConnectMCPConfig_StuckCursorErrors(t *testing.T) {
	ft := map[string]*fakeMCPTransport{"srv": {tools: []mcp.Tool{mcpTool("a")}, stuckCursor: true}}
	cfg := &MCPConfig{MCPServers: map[string]MCPServerConfig{"srv": {Command: "x"}}}
	ts, err := ConnectMCPConfig(context.Background(), cfg, withMCPTransportFactory(fakeFactory(ft)))
	if err == nil || !strings.Contains(err.Error(), "cursor did not advance") {
		t.Fatalf("err = %v, want stuck-cursor error", err)
	}
	defer ts.Close()
	if len(ts.Tools()) != 0 {
		t.Error("expected no tools from a stuck-cursor server")
	}
}

func TestMCPGroupMatches(t *testing.T) {
	if !mcpGroupMatches("firecrawl__scrape", "firecrawl") {
		t.Error("expected group match")
	}
	if mcpGroupMatches("read", "read") {
		t.Error("exact name without separator should not group-match")
	}
	if mcpGroupMatches("firecrawlX__y", "firecrawl") {
		t.Error("prefix without separator boundary should not match")
	}
}
