package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// TestKiroLiveWorkspacePlatformTools drives the real, logged-in kiro-cli in
// workspace mode (the mode a conversation keeps after opening a workspace)
// against the real session MCP gateway, and asserts the model can actually
// reach a platform tool (workbench_tool_schema). This reproduces D3: a
// workspace-mode turn that cannot see any platform tool. Gated like the other
// Kiro smoke tests; costs a few credits.
func TestKiroLiveWorkspacePlatformTools(t *testing.T) {
	if os.Getenv("LOOPFORGE_KIRO_SMOKE") != "1" {
		t.Skip("set LOOPFORGE_KIRO_SMOKE=1 to run against the real kiro-cli")
	}
	if _, err := resolveAssistantEngineBin(runtime.AgentCLIKiro); err != nil || !runtime.KiroLoginAvailable() {
		t.Fatal("kiro-cli or its login missing")
	}
	root := t.TempDir()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 91, LoginName: "kiro-ws"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &AgentToolSession{
		UserID: user.ID, User: user, ChatOwnerID: user.ID, ChatSessionID: "kiro-ws-chat",
		ProjectID: "platform", Mode: AgentToolAssistant, ExecMode: assistantExecModeForApproval(AssistantApprovalConfirm),
		RuntimeMode: domain.AssistantModeWorkspace, ExpiresAt: time.Now().Add(10 * time.Minute), ctx: ctx,
	}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	files, err := writeAgentToolFiles(root, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)

	if err := os.MkdirAll(filepath.Join(root, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	var tools []string
	prompt := "You are in a workspace. Use the workbench_tool_schema tool to fetch the input schema of the platform operation named issues_dispatch, then report the field names it requires. If you have no such tool, reply exactly NO_PLATFORM_TOOLS."
	start := time.Now()
	out, _, err := runAssistantCLI(ctx, assistantCLIRequest{
		Executor: "kiro", Model: "kiro:" + firstNonEmpty(os.Getenv("LOOPFORGE_KIRO_SMOKE_MODEL"), "auto"),
		Prompt: prompt, SessionRoot: filepath.Join(root, "session"), WorkDir: filepath.Join(root, "ws"),
		Native: true, Workspace: true, MCPConfig: files.MCPConfig,
	}, user, func(ev assistantStreamEvent) {
		if ev.Kind == "tool" {
			tools = append(tools, ev.Tool)
		}
	})
	t.Logf("%s tools=%q out=%s err=%v", time.Since(start), tools, truncateRunes(out, 400), err)
	if err != nil {
		t.Fatal(err)
	}
	calledSchema := false
	for _, name := range tools {
		if strings.Contains(strings.ToLower(name), "workbench_tool_schema") || strings.Contains(strings.ToLower(name), "tool_schema") {
			calledSchema = true
		}
	}
	if strings.Contains(out, "NO_PLATFORM_TOOLS") || !calledSchema {
		t.Fatalf("workspace-mode Kiro could not reach platform tools (D3): tools=%q out=%s", tools, out)
	}
}

// TestKiroLiveWorkspaceRelayPlatformTools reproduces the sandbox-equivalent
// relay path: the conversation process rewrites the gateway URL in Kiro's
// mcp.json to a local relay and runs kiro-cli through it, exactly as a
// workspace turn does. It is the path where D3 was observed ("no MCP gateway
// request"). Gated like the other smoke tests.
func TestKiroLiveWorkspaceRelayPlatformTools(t *testing.T) {
	if os.Getenv("LOOPFORGE_KIRO_SMOKE") != "1" {
		t.Skip("set LOOPFORGE_KIRO_SMOKE=1 to run against the real kiro-cli")
	}
	if _, err := resolveAssistantEngineBin(runtime.AgentCLIKiro); err != nil || !runtime.KiroLoginAvailable() {
		t.Fatal("kiro-cli or its login missing")
	}
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "home", "tmp"), filepath.Join(root, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 92, LoginName: "kiro-ws-relay"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s := &AgentToolSession{
		UserID: user.ID, User: user, ChatOwnerID: user.ID, ChatSessionID: "kiro-ws-relay-chat",
		ProjectID: "platform", Mode: AgentToolAssistant, ExecMode: assistantExecModeForApproval(AssistantApprovalConfirm),
		RuntimeMode: domain.AssistantModeWorkspace, ExpiresAt: time.Now().Add(10 * time.Minute), ctx: ctx,
	}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	files, err := writeAgentToolFiles(root, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)

	gatewayHits := 0
	forward := func(fctx context.Context, token string, body json.RawMessage) (json.RawMessage, error) {
		gatewayHits++
		var rpc agentToolRPCRequest
		if err := json.Unmarshal(body, &rpc); err != nil {
			return nil, err
		}
		resp := (&agentToolGateway{deps: &deps{repo: st}}).handleRPC(withAgentToolToken(fctx, token), s, rpc)
		if resp == nil {
			return nil, nil
		}
		return json.Marshal(resp)
	}

	engineOpts := assistantEngineInvocationOpts{
		Bin: "kiro-cli", Root: root, Config: files.MCPConfig, Prompt: prompt92(),
		Model: firstNonEmpty(os.Getenv("LOOPFORGE_KIRO_SMOKE_MODEL"), "auto"), SessionID: "relay1", Workspace: true,
	}
	inv, err := prepareAssistantAgentCLIInvocation(runtime.AgentCLIKiro, engineOpts)
	if err != nil {
		t.Fatal(err)
	}
	command := inv.command.WithPrivateHomeDir(filepath.Join(root, "home"))
	command = runtime.WithCommandEnvironment(command, map[string]string{"AGENTFLOW_NATIVE_APPROVAL_URL": agentToolMCPURL(s.Token)})
	wire := runtime.ConversationCommandFor(command, runtime.RunCredentials{AgentCLI: runtime.AgentCLIUserCredentials(user)})

	pool := &runtime.ConversationProcessPool{}
	if _, err := pool.Handle(ctx, root, filepath.Join(root, "ws"), nil, runtime.ConversationProcessRequest{Operation: "prepare"}, forward); err != nil {
		t.Fatal(err)
	}
	id, err := runtime.NewConversationProcessID()
	if err != nil {
		t.Fatal(err)
	}
	started, err := pool.Handle(ctx, root, filepath.Join(root, "ws"), nil, runtime.ConversationProcessRequest{Operation: "start", ID: id, Command: wire, Token: s.Token, Gateway: config.Current().AgentToolGateway}, forward)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var lines []string
	after := int64(0)
	for {
		res, err := pool.Handle(ctx, root, filepath.Join(root, "ws"), nil, runtime.ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after}, forward)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		for _, ev := range res.Events {
			lines = append(lines, ev.Message)
		}
		after = res.Sequence
		if res.Done {
			if res.Error != "" {
				t.Fatalf("process error: %s\noutput:\n%s", res.Error, strings.Join(lines, "\n"))
			}
			break
		}
	}
	joined := strings.Join(lines, "\n")
	t.Logf("gatewayHits=%d out=%s", gatewayHits, truncateRunes(joined, 600))
	if gatewayHits == 0 {
		t.Fatalf("workspace-mode Kiro made no MCP gateway request through the relay (D3): %s", truncateRunes(joined, 400))
	}
	if !strings.Contains(joined, "workbench_tool_schema") {
		t.Fatalf("workspace-mode Kiro did not call the platform tool through the relay (D3): %s", truncateRunes(joined, 400))
	}
}

func prompt92() string {
	return "You are in a workspace. Use the workbench_tool_schema tool to fetch the input schema of the platform operation named issues_dispatch, then report the required field names. If you have no such tool, reply exactly NO_PLATFORM_TOOLS."
}

// TestKiroLiveWorkspaceResumePlatformTools reproduces the exact D3 condition: a
// conversation whose native Kiro session was created in an earlier turn, then
// continues in workspace mode on a resumed session. The resumed workspace turn
// must still reach the platform tools.
func TestKiroLiveWorkspaceResumePlatformTools(t *testing.T) {
	if os.Getenv("LOOPFORGE_KIRO_SMOKE") != "1" {
		t.Skip("set LOOPFORGE_KIRO_SMOKE=1 to run against the real kiro-cli")
	}
	if _, err := resolveAssistantEngineBin(runtime.AgentCLIKiro); err != nil || !runtime.KiroLoginAvailable() {
		t.Fatal("kiro-cli or its login missing")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 93, LoginName: "kiro-ws-resume"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	t.Cleanup(func() { config.SetCurrent(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s := &AgentToolSession{
		UserID: user.ID, User: user, ChatOwnerID: user.ID, ChatSessionID: "kiro-ws-resume-chat",
		ProjectID: "platform", Mode: AgentToolAssistant, ExecMode: assistantExecModeForApproval(AssistantApprovalConfirm),
		RuntimeMode: domain.AssistantModePlatform, ExpiresAt: time.Now().Add(15 * time.Minute), ctx: ctx,
	}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	files, err := writeAgentToolFiles(root, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.Cleanup)

	model := firstNonEmpty(os.Getenv("LOOPFORGE_KIRO_SMOKE_MODEL"), "auto")
	turn := func(workspace, resume bool, prompt string) (string, []string) {
		var tools []string
		out, _, err := runAssistantCLI(ctx, assistantCLIRequest{
			Executor: "kiro", Model: "kiro:" + model, Prompt: prompt, SessionID: "resume-session",
			SessionRoot: filepath.Join(root, "session"), WorkDir: filepath.Join(root, "ws"),
			Native: true, Workspace: workspace, Resume: resume, MCPConfig: files.MCPConfig,
		}, user, func(ev assistantStreamEvent) {
			if ev.Kind == "tool" {
				tools = append(tools, ev.Tool)
			}
		})
		if err != nil {
			t.Fatalf("turn (workspace=%v resume=%v) failed: %v", workspace, resume, err)
		}
		return out, tools
	}

	// Turn 1: platform mode creates the native session.
	s.RuntimeMode = domain.AssistantModePlatform
	s.mu.Lock()
	s.catalog = nil
	s.mu.Unlock()
	turn(false, false, "Reply with the single word READY.")

	// Turn 2: the conversation is now in workspace mode and resumes the session.
	s.RuntimeMode = domain.AssistantModeWorkspace
	s.mu.Lock()
	s.catalog = nil
	s.mu.Unlock()
	out, tools := turn(true, true, prompt92())
	calledSchema := false
	for _, name := range tools {
		if strings.Contains(strings.ToLower(name), "tool_schema") {
			calledSchema = true
		}
	}
	t.Logf("resumed workspace turn tools=%q out=%s", tools, truncateRunes(out, 300))
	if strings.Contains(out, "NO_PLATFORM_TOOLS") || !calledSchema {
		t.Fatalf("resumed workspace Kiro could not reach platform tools (D3): tools=%q out=%s", tools, out)
	}
}
