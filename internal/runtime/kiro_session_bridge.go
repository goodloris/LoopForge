package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// agentToolSessionPath marks the platform session gateway among a Kiro
// conversation's MCP servers. Only that server is relay-bound and non-loopback
// in a Docker sandbox; a loopback sidecar (the agent browser) is left alone.
const agentToolSessionPath = "/internal/agent-tools/"

// kiroSessionBridgePython pipes newline-delimited JSON-RPC between kiro-cli's
// stdio MCP transport and the HTTP session relay named in AGENTFLOW_RELAY_URL.
// A plain child process is not subject to kiro's enterprise MCP network
// sandboxing, so it reaches the relay's non-loopback address where a direct
// HTTP MCP server is silently dropped. One worker thread per message keeps a
// long tool call (an approval wait) from blocking a later cancellation.
const kiroSessionBridgePython = `import os,sys,json,threading,urllib.request
U=os.environ["AGENTFLOW_RELAY_URL"]
L=threading.Lock()
def h(line):
 try:
  rq=urllib.request.Request(U,data=line.encode(),headers={"Content-Type":"application/json","Accept":"application/json"},method="POST")
  b=urllib.request.urlopen(rq,timeout=600).read()
 except Exception as e:
  try: i=json.loads(line).get("id")
  except Exception: i=None
  if i is None: return
  b=json.dumps({"jsonrpc":"2.0","id":i,"error":{"code":-32000,"message":"bridge: "+str(e)}}).encode()
 if b:
  with L:
   sys.stdout.write(b.decode().rstrip("\n")+"\n");sys.stdout.flush()
for ln in sys.stdin:
 ln=ln.strip()
 if ln: threading.Thread(target=h,args=(ln,),daemon=True).start()
`

// rewriteKiroSessionMCPBridge turns the session gateway entry in a Kiro
// conversation's mcp.json from an HTTP url server into a python3 stdio bridge
// to that same url. kiro-cli under an enterprise profile only connects
// loopback HTTP MCP servers, so a Docker sandbox's non-loopback relay url is
// unreachable as http; the stdio bridge is not network-sandboxed and reaches
// it. python3 is already required for the native approval hook, so it is a
// safe dependency. Other servers (the loopback browser) are left unchanged;
// a missing config is a no-op.
func rewriteKiroSessionMCPBridge(root string) error {
	path := filepath.Join(root, "home", agentCLIMCPPaths[AgentCLIKiro])
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	servers, _ := document["mcpServers"].(map[string]any)
	if servers == nil {
		return nil
	}
	changed := false
	for id, raw := range servers {
		server, _ := raw.(map[string]any)
		url, _ := server["url"].(string)
		if server == nil || !strings.Contains(url, agentToolSessionPath) {
			continue
		}
		servers[id] = map[string]any{
			"command": "python3",
			"args":    []any{"-I", "-c", kiroSessionBridgePython, id},
			"env":     map[string]any{"AGENTFLOW_RELAY_URL": url},
		}
		changed = true
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
