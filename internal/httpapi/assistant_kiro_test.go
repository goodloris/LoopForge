package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// Kiro CLI chats on the machine's kiro-cli login: no key, a read-only trust
// list naming the session MCP server, and no Docker workspace.
func TestAssistantKiroChatInvocation(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(config, []byte(`{"mcpServers":{"agentflow_session":{"url":"http://127.0.0.1/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := assistantEngineInvocationOpts{Bin: "kiro-cli", Root: root, Config: config, Prompt: "hi", Model: "auto", SessionID: "s1"}
	inv, err := prepareAssistantAgentCLIInvocation(runtime.AgentCLIKiro, opts)
	if err != nil {
		t.Fatal(err)
	}
	argv := inv.command.Argv
	if !slices.Contains(argv, "--trust-tools=read,grep,glob,disclose_context,invoke_sub_agent,@agentflow_session") || slices.Contains(argv, "--trust-all-tools") {
		t.Fatalf("argv = %q", argv)
	}
	if _, err := os.Stat(filepath.Join(root, "home", ".kiro", "settings", "mcp.json")); err != nil {
		t.Fatalf("MCP config not in KIRO_HOME: %v", err)
	}
	opts.Workspace = true
	inv, err = prepareAssistantAgentCLIInvocation(runtime.AgentCLIKiro, opts)
	if err != nil {
		t.Fatal(err)
	}
	// A conversation workspace trusts the full built-in tool set via @builtin
	// (no maintained list), but headless Kiro only connects an MCP server it
	// can name, so the session server is trusted explicitly and never via bare
	// --trust-all-tools (D3).
	if slices.Contains(inv.command.Argv, "--trust-all-tools") {
		t.Fatalf("workspace must name the MCP server, not use --trust-all-tools: %q", inv.command.Argv)
	}
	wtrust := ""
	for _, arg := range inv.command.Argv {
		if strings.HasPrefix(arg, "--trust-tools=") {
			wtrust = arg
		}
	}
	for _, want := range []string{"@builtin", "@agentflow_session"} {
		if !slices.Contains(strings.Split(strings.TrimPrefix(wtrust, "--trust-tools="), ","), want) {
			t.Fatalf("workspace trust missing %q: %q", want, inv.command.Argv)
		}
	}
}

func TestAssistantKiroConfiguredWhereInstalled(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	if key, _ := assistantEngineCredential(domain.User{}, runtime.AgentCLIKiro); key != "" {
		t.Fatal("Kiro must not be offered without kiro-cli")
	}
	kiroCLIInstalled = func() bool { return true }
	if key, _ := assistantEngineCredential(domain.User{}, runtime.AgentCLIKiro); key == "" {
		t.Fatal("Kiro needs no API key")
	}
}

// A turn is never refused for Kiro: the conversation may run on an execution
// machine other than this server or the user's, which reports a missing
// kiro-cli or login itself.
func TestAssistantKiroTurnNotRefusedUpFront(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	req := assistantRequest{Executor: runtime.AgentCLIKiro, Model: "auto"}
	if err := validateAssistantProvider(&req, domain.User{}); err != nil {
		t.Fatal(err)
	}
	if _, _, refusal := (&assistantHandlers{}).assistantTurnUser(domain.User{}, "kiro:auto", false); refusal != "" {
		t.Fatal(refusal)
	}
}

// Kiro hooks name session tools mcp_<server>_<tool> and load them through
// tool_load; both pass, native effects stay denied in platform mode.
func TestNativeApprovalKiroToolNames(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModePlatform, ctx: context.Background()}
	for _, allowed := range []string{
		`{"name":"mcp_agentflow_session_list_projects","input":{}}`,
		`{"name":"tool_load","input":{"tool_ids":["agentflow_session::list_projects"]}}`,
		`{"name":"read_file","input":{"path":"a"}}`,
	} {
		if result := g.approveNativeTool(context.Background(), s, json.RawMessage(allowed)); !result.Allow {
			t.Fatalf("denied %s: %s", allowed, result.Reason)
		}
	}
	if result := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"execute_bash","input":{"command":"echo x"}}`)); result.Allow {
		t.Fatal("platform native shell permitted")
	}
}

// A role reaches Kiro as a V3 Markdown agent in KIRO_HOME/agents: Main Bot's
// own tools excluded by @server/tool, its skills as skill:// resources.
func TestKiroRoleAgentFile(t *testing.T) {
	role := roleSubagent{Role: domain.AgentDefinition{ID: "Reviewer", Name: "评审", Prompt: "检查代码"}, Skills: []domain.SkillDefinition{{ID: "lint"}}}
	file := kiroRoleAgentFile(role, false)
	for _, want := range []string{
		"name: role-reviewer\n", `tools: ["*"]`, `"@agentflow_session/` + submitAssistantReplyTool + `"`,
		`"@agentflow_session/workbench_workspace_open"`, `"skill://` + runtime.SkillName("lint") + `"`, "检查代码",
	} {
		if !strings.Contains(file, want) {
			t.Fatalf("missing %q in:\n%s", want, file)
		}
	}
	if strings.Contains(file, "disallowedTools") || strings.Count(file, "\n---\n") != 1 {
		t.Fatalf("one Kiro front matter expected:\n%s", file)
	}
}

// Chat adds up invocations, so a Kiro result carries this invocation's
// credits, not the session total an SOP node's cursor needs.
func TestKiroChatResultUsesTurnCredits(t *testing.T) {
	var native string
	line := assistantAgentCLILine(runtime.AgentCLIKiro, `{"type":"result","result":"ok","usage":{"credits":0.5,"turn_credits":0.02}}`, &native)
	usage := runtime.ParseTokenUsage(func() any {
		var v struct{ Usage any }
		_ = json.Unmarshal([]byte(line), &v)
		return v.Usage
	}())
	if usage.Credits != 0.02 || strings.Contains(line, "turn_credits") {
		t.Fatalf("line=%s credits=%v", line, usage.Credits)
	}
}

// Kiro counts as configured when the user's default execution machine is
// online with kiro-cli, even where the platform has none.
func TestKiroConfiguredByOwnRunner(t *testing.T) {
	defer func(previous func(int64) bool) { kiroRunnerReady = previous }(kiroRunnerReady)
	kiroRunnerReady = func(id int64) bool { return id == 7 }
	if key, _ := assistantEngineCredential(domain.User{ID: 7}, runtime.AgentCLIKiro); key != kiroMachineLogin {
		t.Fatal("runner with kiro must count as configured")
	}
	if key, _ := assistantEngineCredential(domain.User{ID: 8}, runtime.AgentCLIKiro); key != "" {
		t.Fatal("no kiro anywhere must stay unconfigured")
	}
	if !strings.Contains(assistantEngineMissingKey(runtime.AgentCLIKiro), "kiro-cli login") {
		t.Fatal("Kiro needs a login hint, not an API key one")
	}
}

// An unavailable CLI is refused, never rewritten to CodeBuddy.
func TestUnavailableCLIRefusedNotFallenBack(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	user := domain.User{ID: 9}
	if got := assistantUnavailableCLIRefusal("kiro:default", user); got != assistantEngineMissingKey(runtime.AgentCLIKiro) {
		t.Fatalf("refusal=%q", got)
	}
	if got := assistantUnavailableCLIRefusal("personal-a", user); got != "" {
		t.Fatalf("CodeBuddy must not be refused here: %q", got)
	}
	kiroCLIInstalled = func() bool { return true }
	if got := assistantUnavailableCLIRefusal("kiro:default", user); got != "" {
		t.Fatalf("available CLI refused: %q", got)
	}
}
