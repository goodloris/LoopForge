package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An entry without arguments (the gate MCP server) carries a nil slice; it
// must be omitted, not written as null.
func TestWriteAgentCLIMCPConfigOmitsNilFields(t *testing.T) {
	entries := map[string]any{"agentflow-gate": map[string]any{"command": ".agentflow/gate-mcp", "args": []string(nil), "env": map[string]string{"A": "1"}}}
	for _, kind := range []string{AgentCLICursor, AgentCLIKimi, AgentCLIOpenCode, AgentCLIDeepSeek, AgentCLIKiro} {
		home := t.TempDir()
		if err := WriteAgentCLIMCPConfig(kind, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(home, agentCLIMCPPaths[kind]))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "null") || !strings.Contains(string(data), `"A"`) {
			t.Fatalf("%s: %s", kind, data)
		}
	}
}
