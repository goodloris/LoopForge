package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRewriteKiroSessionMCPBridge locks the Docker+Kiro fix: the non-loopback
// session gateway becomes a python3 stdio bridge (kiro-cli under an enterprise
// profile only dials loopback HTTP MCP), while a loopback sidecar stays HTTP.
// Reverting the rewrite leaves the session server as "url" and fails here.
func TestRewriteKiroSessionMCPBridge(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "home", ".kiro", "settings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	relayURL := "http://172.21.0.3:42977/internal/agent-tools/deadbeef/mcp"
	browserURL := "http://127.0.0.1:8931/mcp"
	input := `{"mcpServers":{"agentflow_session":{"url":"` + relayURL + `"},"agentflow-browser":{"url":"` + browserURL + `"}}}`
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := rewriteKiroSessionMCPBridge(root); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	session := doc.MCPServers["agentflow_session"]
	if _, ok := session["url"]; ok {
		t.Fatalf("session server still has a url (the non-loopback relay kiro refuses): %v", session)
	}
	if session["command"] != "python3" {
		t.Fatalf("session server command = %v, want python3", session["command"])
	}
	env, _ := session["env"].(map[string]any)
	if env["AGENTFLOW_RELAY_URL"] != relayURL {
		t.Fatalf("bridge AGENTFLOW_RELAY_URL = %v, want %q", env["AGENTFLOW_RELAY_URL"], relayURL)
	}
	args, _ := session["args"].([]any)
	joined := ""
	for _, a := range args {
		joined += " " + a.(string)
	}
	if !strings.Contains(joined, "-c") || !strings.Contains(joined, "AGENTFLOW_RELAY_URL") {
		t.Fatalf("bridge args missing the inline python bridge: %v", args)
	}

	// The loopback sidecar must stay an HTTP server (kiro connects loopback
	// HTTP fine); converting it would be wrong.
	browser := doc.MCPServers["agentflow-browser"]
	if browser["url"] != browserURL {
		t.Fatalf("loopback browser server was altered: %v", browser)
	}
}

// TestRewriteKiroSessionMCPBridgeNoConfig is a no-op when the sandbox has no
// kiro mcp.json yet (a turn that configured no servers).
func TestRewriteKiroSessionMCPBridgeNoConfig(t *testing.T) {
	if err := rewriteKiroSessionMCPBridge(t.TempDir()); err != nil {
		t.Fatalf("missing config should be a no-op, got %v", err)
	}
}
