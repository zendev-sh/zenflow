package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/mcp"
	"github.com/zendev-sh/zenflow"
)

// mcpHTTPServer is a minimal in-process MCP server over Streamable HTTP. It
// answers goai's connect handshake (GET -> 405 = POST-only), then initialize
// and tools/list over POST so connectMCPServers can connect for real without
// a subprocess or the network.
func mcpHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// No server-initiated SSE stream; goai treats 405 as POST-only.
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req mcp.JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.ID == nil { // notification (e.g. notifications/initialized)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = mcp.InitializeResult{
				ProtocolVersion: mcp.ProtocolVersion20250326,
				Capabilities:    mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}},
				ServerInfo:      mcp.ServerInfo{Name: "everything", Version: "1.0"},
			}
		case "tools/list":
			result = mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
			}}
		default:
			result = map[string]any{}
		}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mcp.JSONRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: raw})
	}))
}

// captureStderr redirects the package stderr to a buffer for the test.
func captureStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = prev })
	return &buf
}

// mcpNamesOf returns the names of a tool slice (for assertions).
func mcpNamesOf(tools []goai.Tool) []string {
	n := make([]string, len(tools))
	for i, x := range tools {
		n[i] = x.Name
	}
	return n
}

// writeDefaultMCPConfig drops a .zenflow/settings.json into a fresh dir and
// chdirs there, so connectMCPServers finds it via the default path.
func writeDefaultMCPConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zenflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, defaultMCPConfigPath), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
}

// withStubMCPLoad swaps the mcpLoad seam for the duration of a test.
func withStubMCPLoad(t *testing.T, fn func(cmdFlags) mcpResult) {
	t.Helper()
	prev := mcpLoad
	mcpLoad = fn
	t.Cleanup(func() { mcpLoad = prev })
}

func TestLoadMCPOptions_ActiveAppendsTools(t *testing.T) {
	cleaned := false
	withStubMCPLoad(t, func(cmdFlags) mcpResult {
		return mcpResult{
			tools:   []goai.Tool{{Name: "firecrawl__scrape"}},
			servers: []string{"firecrawl"},
			cleanup: func() { cleaned = true },
			active:  true,
		}
	})
	buf := captureStderr(t)

	opts, cleanup := loadMCPOptions(cmdFlags{verbose: true})
	if len(opts) != 1 {
		t.Fatalf("opts = %d, want 1 (WithAdditionalTools)", len(opts))
	}
	if !strings.Contains(buf.String(), "MCP loaded 1 tool(s) from server(s) [firecrawl]") {
		t.Errorf("verbose log missing, got %q", buf.String())
	}
	cleanup()
	if !cleaned {
		t.Error("cleanup not invoked")
	}
}

func TestLoadMCPOptions_InactiveNoOpts(t *testing.T) {
	withStubMCPLoad(t, func(cmdFlags) mcpResult { return mcpInactive })
	opts, cleanup := loadMCPOptions(cmdFlags{})
	if opts != nil {
		t.Errorf("opts = %v, want nil when inactive", opts)
	}
	cleanup() // must be safe
}

func TestConnectMCPServers_SuccessLoadsTools(t *testing.T) {
	srv := mcpHTTPServer(t)
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	body := `{"mcpServers":{"everything":{"url":"` + srv.URL + `"}}}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureStderr(t)

	// verbose exercises the stderr subprocess-routing option path too.
	r := connectMCPServers(cmdFlags{mcpConfig: cfgPath, verbose: true})
	defer r.cleanup()
	if !r.active {
		t.Fatalf("expected active=true; stderr=%q", buf.String())
	}
	if len(r.tools) != 1 || r.tools[0].Name != "everything__echo" {
		t.Fatalf("tools = %v, want [everything__echo]", mcpNamesOf(r.tools))
	}
	if len(r.servers) != 1 || r.servers[0] != "everything" {
		t.Fatalf("servers = %v, want [everything]", r.servers)
	}
}

func TestConnectMCPServers_DefaultPathAutoLoads(t *testing.T) {
	srv := mcpHTTPServer(t)
	defer srv.Close()
	// An auto-discovered .zenflow/settings.json loads without any prompt.
	writeDefaultMCPConfig(t, `{"mcpServers":{"everything":{"url":"`+srv.URL+`"}}}`)
	captureStderr(t)

	r := connectMCPServers(cmdFlags{})
	defer r.cleanup()
	if !r.active || len(r.tools) != 1 {
		t.Fatalf("expected auto-load from default path, got active=%v tools=%v", r.active, mcpNamesOf(r.tools))
	}
}

func TestConnectMCPServers_NoMCPFlag(t *testing.T) {
	r := connectMCPServers(cmdFlags{noMCP: true})
	if r.active || r.tools != nil || r.servers != nil {
		t.Error("expected inactive with --no-mcp")
	}
	r.cleanup()
}

func TestConnectMCPServers_MissingDefaultSilent(t *testing.T) {
	chdir(t, t.TempDir()) // empty dir => default .zenflow/settings.json absent
	buf := captureStderr(t)

	r := connectMCPServers(cmdFlags{})
	if r.active {
		t.Error("expected inactive when default config absent")
	}
	if buf.Len() != 0 {
		t.Errorf("missing default config should be silent, got %q", buf.String())
	}
}

func TestConnectMCPServers_MissingExplicitWarns(t *testing.T) {
	buf := captureStderr(t)
	r := connectMCPServers(cmdFlags{mcpConfig: filepath.Join(t.TempDir(), "nope.json")})
	if r.active {
		t.Error("expected inactive")
	}
	if !strings.Contains(buf.String(), "MCP config not found") {
		t.Errorf("expected not-found warning, got %q", buf.String())
	}
}

func TestConnectMCPServers_MalformedWarns(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureStderr(t)
	r := connectMCPServers(cmdFlags{mcpConfig: bad})
	if r.active {
		t.Error("expected inactive on malformed config")
	}
	if !strings.Contains(buf.String(), "MCP config:") {
		t.Errorf("expected parse warning, got %q", buf.String())
	}
}

func TestConnectMCPServers_ConnectFailureNoTools(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	// A server whose command does not exist: connection fails, no tools.
	body := `{"mcpServers":{"ghost":{"command":"zenflow-no-such-mcp-binary-xyz"}}}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := captureStderr(t)
	r := connectMCPServers(cmdFlags{mcpConfig: cfgPath, verbose: true})
	if r.active || len(r.tools) != 0 {
		t.Error("expected inactive with no tools when the server fails to start")
	}
	if !strings.Contains(buf.String(), "zenflow: MCP:") {
		t.Errorf("expected connect-failure warning, got %q", buf.String())
	}
	r.cleanup()
}

func TestWarnInsecureMCP(t *testing.T) {
	buf := captureStderr(t)
	warnInsecureMCP(&zenflow.MCPConfig{MCPServers: map[string]zenflow.MCPServerConfig{
		"plain":    {URL: "http://x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}},
		"secure":   {URL: "https://x/mcp", Headers: map[string]string{"Authorization": "Bearer t"}},
		"noheader": {URL: "http://x/mcp"},
		"off":      {URL: "http://x/mcp", Headers: map[string]string{"A": "b"}, Disabled: true},
	}})
	out := buf.String()
	if !strings.Contains(out, `"plain"`) {
		t.Errorf("expected warning for plaintext+headers server, got %q", out)
	}
	if strings.Contains(out, `"secure"`) || strings.Contains(out, `"noheader"`) || strings.Contains(out, `"off"`) {
		t.Errorf("unexpected warning, got %q", out)
	}
}

// chdir changes to dir for the duration of the test, restoring the prior cwd.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
