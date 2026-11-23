package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentCLICommandKiro(t *testing.T) {
	got := AgentCLICommand(AgentCLIKiro, AgentCLICommandOptions{Model: "claude-haiku-5.5", Effort: "high", Resume: "sess_1", Prompt: "task", SystemPrompt: "sys"}).Argv
	want := []string{"kiro-cli", "chat", "--v3", "--no-interactive", "--output-format", "stream-json", "--trust-all-tools", "--model", "claude-haiku-5.5", "--effort", "high", "--resume-id", "sess_1", "<system_instructions>\nsys\n</system_instructions>\n\ntask"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %q", got)
	}
	chat := AgentCLICommand(AgentCLIKiro, AgentCLICommandOptions{Prompt: "q", Chat: true, MCPServers: []string{"agentflow_session", "bad id"}}).Argv
	if !slices.Contains(chat, "--trust-tools=read,grep,glob,disclose_context,invoke_sub_agent,@agentflow_session") || slices.Contains(chat, "--trust-all-tools") {
		t.Fatalf("chat must trust only read tools and the MCP servers: %q", chat)
	}
	clarify := AgentCLICommand(AgentCLIKiro, AgentCLICommandOptions{Prompt: "q", Chat: true, MCPOnly: true, ReadRoots: []string{"/srv/checkout"}, MCPServers: []string{"agentflow_session"}}).Argv
	if !slices.Contains(clarify, "--trust-tools=read,grep,glob,@agentflow_session") {
		t.Fatalf("clarification must trust only the file read tools and MCP: %q", clarify)
	}
	// A conversation workspace turn is a full coding agent, but headless Kiro
	// only connects an MCP server it can name in --trust-tools: the bare
	// --trust-all-tools leaves the platform session server unloaded (D3). So
	// workspace trust uses @builtin (every built-in tool, no maintained list)
	// and names every MCP server, and never falls back to --trust-all-tools.
	workspace := AgentCLICommand(AgentCLIKiro, AgentCLICommandOptions{Prompt: "q", Chat: true, Workspace: true, MCPServers: []string{"agentflow_session", "bad id"}}).Argv
	if slices.Contains(workspace, "--trust-all-tools") {
		t.Fatalf("workspace must not use --trust-all-tools (MCP server stays unloaded): %q", workspace)
	}
	wtrust := kiroTrustArg(t, workspace)
	wtools := strings.Split(strings.TrimPrefix(wtrust, "--trust-tools="), ",")
	for _, want := range []string{"@builtin", "@agentflow_session"} {
		if !slices.Contains(wtools, want) {
			t.Fatalf("workspace trust missing %q: %q", want, wtrust)
		}
	}
	if strings.Contains(wtrust, "bad id") {
		t.Fatalf("invalid MCP server id leaked into workspace trust: %q", wtrust)
	}
}

// kiroTrustArg returns the --trust-tools value from a recorded kiro-cli argv.
func kiroTrustArg(t *testing.T, argv []string) string {
	t.Helper()
	for _, arg := range argv {
		if strings.HasPrefix(arg, "--trust-tools=") {
			return arg
		}
	}
	t.Fatalf("no --trust-tools in %q", argv)
	return ""
}

type kiroHookDoc struct {
	Hooks []struct {
		Trigger string          `json:"trigger"`
		Action  json.RawMessage `json:"action"`
	} `json:"hooks"`
}

func installKiroApprovalHook(t *testing.T) (root, command string, data []byte) {
	t.Helper()
	root = t.TempDir()
	if _, err := InstallConversationApproval(root, AgentCLIKiro, CommandSpec{Argv: []string{"kiro-cli"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "home", ".kiro", "hooks", "loopforge-approval.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc kiroHookDoc
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Hooks) != 1 || doc.Hooks[0].Trigger != "PreToolUse" {
		t.Fatalf("hook: %s", data)
	}
	var action struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(doc.Hooks[0].Action, &action); err != nil {
		t.Fatal(err)
	}
	return root, action.Command, data
}

// AI 对话 is gated by a global PreToolUse hook in KIRO_HOME. Kiro runs the
// command through a shell and blocks only on exit 2: every other outcome,
// a missing or crashed interpreter included, must still become 2.
func TestKiroConversationApprovalHook(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	var mu sync.Mutex
	decision := `{"result":{"allow":true}}`
	pendingUntil := time.Time{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body := decision
		if time.Now().Before(pendingUntil) {
			body = `{"result":{"pending":true}}`
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	root, command, data := installKiroApprovalHook(t)
	// Kiro reads the timeout from the hook document and ignores one in
	// action; without it a hook waiting for the user is killed after 60 s
	// and the tool runs.
	var doc struct {
		Hooks []struct {
			Timeout *int `json:"timeout"`
			Action  struct {
				Timeout *int `json:"timeout"`
			} `json:"action"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Hooks[0].Timeout == nil || *doc.Hooks[0].Timeout != conversationApprovalHookWatchdog || doc.Hooks[0].Action.Timeout != nil {
		t.Fatalf("Kiro would kill the approval wait after its 60 s default and run the tool: %s", data)
	}
	run := func(path string) (int, string) {
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Env = []string{"PATH=" + path, "AGENTFLOW_NATIVE_APPROVAL_URL=" + server.URL}
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"execute_bash","tool_input":{"command":"git push"}}`)
		var stdout strings.Builder
		cmd.Stdout = &stdout
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), stdout.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, stdout.String()
	}
	pythonDir := filepath.Dir(python)
	// Kiro adds hook stdout to the model context, so allow prints nothing.
	if code, out := run(pythonDir); code != 0 || out != "" {
		t.Fatalf("approved action exited %d with stdout %q", code, out)
	}
	// Waiting for the user blocks, then the decision applies.
	mu.Lock()
	pendingUntil, decision = time.Now().Add(2*time.Second), `{"result":{"allow":false,"reason":"no"}}`
	mu.Unlock()
	began := time.Now()
	if code, _ := run(pythonDir); code != 2 || time.Since(began) < 2*time.Second {
		t.Fatalf("pending then denied action exited %d after %s", code, time.Since(began))
	}
	if code, _ := run(t.TempDir()); code != 2 {
		t.Fatalf("missing interpreter exited %d", code)
	}
	if err := os.WriteFile(filepath.Join(root, "native-approval.py"), []byte("import os\nos._exit(1)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(pythonDir); code != 2 {
		t.Fatalf("crashed hook exited %d", code)
	}
}

// LOOPFORGE_KIRO_E2E=1 runs the real kiro-cli (logged in, consumes a few
// credits) against the generated hook while the gateway keeps the approval
// pending past Kiro's 60 s default hook timeout, then denies: the shell
// command must not run. A second turn approves and the command must run.
func TestKiroConversationApprovalHookRealCLI(t *testing.T) {
	if os.Getenv("LOOPFORGE_KIRO_E2E") != "1" {
		t.Skip("set LOOPFORGE_KIRO_E2E=1 to run the real kiro-cli probe")
	}
	kiro, err := exec.LookPath("kiro-cli")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls int
	var allow bool
	var pendingUntil time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		body := `{"result":{"allow":false,"reason":"probe denied"}}`
		if time.Now().Before(pendingUntil) {
			body = `{"result":{"pending":true}}`
		} else if allow {
			body = `{"result":{"allow":true}}`
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	root, _, _ := installKiroApprovalHook(t)
	turn := func(probe string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, kiro, "chat", "--v3", "--no-interactive", "--trust-all-tools",
			"Run exactly this shell command once and do not retry: touch "+probe+" . Then reply done.")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "KIRO_HOME="+filepath.Join(root, "home", ".kiro"), "KIRO_NO_AUTO_UPDATE=1", "AGENTFLOW_NATIVE_APPROVAL_URL="+server.URL)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Logf("kiro-cli: %v\n%s", err, output)
		}
	}
	denied := filepath.Join(root, "denied")
	mu.Lock()
	pendingUntil = time.Now().Add(75 * time.Second)
	mu.Unlock()
	turn(denied)
	if _, err := os.Stat(denied); err == nil {
		t.Fatal("kiro-cli ran the command without approval")
	}
	mu.Lock()
	if calls == 0 {
		mu.Unlock()
		t.Fatal("hook never asked the gateway")
	}
	calls, allow = 0, true
	mu.Unlock()
	approved := filepath.Join(root, "approved")
	turn(approved)
	if _, err := os.Stat(approved); err != nil {
		t.Fatal("kiro-cli did not run the approved command")
	}
}

func TestApplyAgentCLICredentialKiroUsesMachineLogin(t *testing.T) {
	argv := []string{"kiro-cli", "chat", "x"}
	gotArgv, env, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{}, HostAgentCLIHome(t.TempDir()), "/h", argv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgv, argv) || env["KIRO_HOME"] != "/h/.kiro" || env["KIRO_API_KEY"] != "" {
		t.Fatalf("argv = %q env = %v", gotArgv, env)
	}
	if !slices.Contains(agentCLIHostEnv, "KIRO_API_KEY") || !slices.Contains(agentCLIHostEnv, "KIRO_HOME") {
		t.Fatal("a host Kiro key or home must not reach a run")
	}
	if requirements := ProviderRequirements(AgentCLIKiro); !requirements.PrivateHome {
		t.Fatalf("requirements = %+v", requirements)
	}
}

// ReadRoots deny file reads outside the roots through permissions.yaml; a
// later run without roots removes the policy again.
func TestApplyKiroReadRoots(t *testing.T) {
	dir := t.TempDir()
	home := HostAgentCLIHome(dir)
	if _, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{ReadRoots: []string{"/srv/checkout/", "relative", "/"}}, home, "/h", []string{"kiro-cli"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".kiro", "settings", "permissions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Rules []struct {
			Capability, Effect string
			Match, Exclude     []string
		}
	}
	if err := json.Unmarshal(data, &policy); err != nil || len(policy.Rules) != 1 {
		t.Fatalf("policy %s: %v", data, err)
	}
	rule := policy.Rules[0]
	if rule.Capability != "fs_read" || rule.Effect != "deny" || !slices.Equal(rule.Match, []string{"/**"}) || !slices.Equal(rule.Exclude, []string{"/srv/checkout", "/srv/checkout/**"}) {
		t.Fatalf("rule = %+v", rule)
	}
	if _, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{}, home, "/h", []string{"kiro-cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".kiro", "settings", "permissions.yaml")); !os.IsNotExist(err) {
		t.Fatalf("policy left behind: %v", err)
	}
}

// The subscription login lives under the machine's real HOME, so the bound
// command keeps it and moves only ~/.kiro into the private home.
func TestBindAgentCLICommandKiroKeepsMachineHome(t *testing.T) {
	workspace := t.TempDir()
	command := CommandSpec{Argv: []string{"kiro-cli"}, Provider: AgentCLIKiro, PrivateHome: true, PrivateHomeKey: "kiro"}
	_, env, err := BindAgentCLICommand(&localSandbox{id: "kiro", workspace: workspace}, workspace, command, ProviderCredential{}, command.Argv)
	if err != nil {
		t.Fatal(err)
	}
	if home, _ := os.UserHomeDir(); env["HOME"] != home {
		t.Fatalf("HOME must stay the machine's own: %v", env)
	}
	if !strings.HasPrefix(env["KIRO_HOME"], workspace) || !strings.HasSuffix(env["KIRO_HOME"], "/.kiro") {
		t.Fatalf("KIRO_HOME = %q", env["KIRO_HOME"])
	}
}

func TestWriteAgentCLIMCPConfigKiro(t *testing.T) {
	home := t.TempDir()
	entries := map[string]any{
		"platform": map[string]any{"type": "http", "url": "http://127.0.0.1/mcp", "headers": map[string]any{"Authorization": "Bearer t"}},
		"local":    map[string]any{"command": "node", "args": []string{"s.js"}},
	}
	if err := WriteAgentCLIMCPConfig(AgentCLIKiro, HostAgentCLIHome(home), entries, AgentCLIMCPOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".kiro", "settings", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCPServers["platform"]["url"] != "http://127.0.0.1/mcp" || doc.MCPServers["platform"]["headers"] == nil || doc.MCPServers["local"]["command"] != "node" {
		t.Fatalf("mcp.json: %s", data)
	}
}

// Lines captured from kiro-cli 3.3.0 (`chat --v3 --output-format stream-json`).
func TestAgentCLITranslatorKiro(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIKiro)
	var lines []string
	for _, raw := range []string{
		`{"type":"runStarted","data":{"payloadSchema":"acp","acpProtocolVersion":1,"engine":"v3"}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_start"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"The user"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":" wants a tool"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"tool_call","toolCallId":"toolu_bdrk_02","title":"@probe/read_secret","rawInput":{},"_meta":{"kiro":{"serverName":"probe","toolOrigin":"default"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"tool_call_update","toolCallId":"toolu_bdrk_02","status":"in_progress","rawInput":{},"_meta":{"kiro":{"serverName":"probe"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"tool_call_update","toolCallId":"toolu_bdrk_02","status":"completed","title":"@probe/read_secret","content":[{"type":"content","content":{"type":"text","text":"secret=42"}}],"rawOutput":{"response":"secret=42"},"_meta":{"kiro":{"serverName":"probe"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"tool_call","toolCallId":"run_command_toolu_bdrk_03","title":"Print hi","kind":"execute","rawInput":{"command":"echo hi"},"_meta":{"kiro":{"toolOrigin":"default"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"tool_call_update","toolCallId":"run_command_toolu_bdrk_03","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Exit Code: 1"}}]}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"4"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"2"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"promptTurnSummaries":[{"unit":"credit","unitPlural":"credits","usage":0.0224}],"kind":"turn_completion"}}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"sess_c3","update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"session_usage","credits":[{"usage":0.5,"unit":"credit","unitPlural":"credits"}]}}}}}`,
		`{"type":"runFinished","data":{"sessionId":"sess_c3","status":"success","stopReason":"end_turn","finalText":"42","finalTextTruncated":false}}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], `"subtype":"init"`) || !strings.Contains(lines[0], `"session_id":"sess_c3"`) || strings.Count(joined, `"subtype":"init"`) != 1 {
		t.Fatalf("init: %s", joined)
	}
	for _, want := range []string{
		`"thinking":"The user wants a tool"`,
		`"name":"mcp__probe__read_secret"`,
		`"content":"secret=42","is_error":false,"tool_use_id":"toolu_bdrk_02"`,
		`"input":{"command":"echo hi"},"name":"run_command"`,
		`"content":"Exit Code: 1","is_error":true,"tool_use_id":"run_command_toolu_bdrk_03"`,
		`"text":"42"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, `"type":"tool_use"`) != 2 || strings.Count(joined, `"tool_use_id":"toolu_bdrk_02"`) != 1 {
		t.Fatalf("tool calls must be reported once:\n%s", joined)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"type":"result"`) || !strings.Contains(last, `"is_error":false`) || !strings.Contains(last, `"result":"42"`) || !strings.Contains(last, `"credits":0.5`) || !strings.Contains(last, `"turn_credits":0.0224`) {
		t.Fatalf("result: %s", last)
	}
}

func TestAgentCLITranslatorKiroRunError(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIKiro)
	var lines []string
	for _, raw := range []string{
		`{"type":"sessionUpdate","data":{"sessionId":"sess_fc","update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"displayError":{"message":"The selected model is not available.","errorType":"InvalidModelError"},"kind":"display_error"}}}}}`,
		`{"type":"runError","data":{"sessionId":"sess_fc","stage":"prompt","message":"The model 'no-such-model' is not available."}}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"is_error":true`) || !strings.Contains(last, "no-such-model") {
		t.Fatalf("result: %s", last)
	}
}

func TestAgentCLITranslatorKiroMapsSubagentsSkillsAndHidesLoaders(t *testing.T) {
	translate := NewAgentCLITranslator(AgentCLIKiro)
	var lines []string
	for _, raw := range []string{
		`{"type":"sessionUpdate","data":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"toolu_1","title":"Tool Load","status":"in_progress","rawInput":{"id":"tool::probe::read_secret"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"toolu_1","status":"completed","title":"Tool Load","rawInput":{"id":"tool::probe::read_secret"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"toolu_2","title":"Load skill: secret-word","rawInput":{"id":"skill::secret-word"}}}}`,
		`{"type":"sessionUpdate","data":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"invoke_subagent_toolu_3","title":"Sub-agent: role-reviewer","rawInput":{"name":"role-reviewer","prompt":"review"}}}}`,
	} {
		lines = append(lines, translate(raw)...)
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "toolu_1") {
		t.Fatalf("tool loading must stay hidden:\n%s", joined)
	}
	for _, want := range []string{`"input":{"skill":"secret-word"},"name":"Skill"`, `"input":{"prompt":"review","subagent_type":"role-reviewer"},"name":"Task"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in:\n%s", want, joined)
		}
	}
}

// Kiro role agents and skills travel to the execution machine with the
// other conversation assets.
func TestKiroConversationAssetsSync(t *testing.T) {
	for _, rel := range []string{"home/.kiro/agents/role-x.md", "home/.kiro/skills/s/SKILL.md"} {
		if allowed, _ := ConversationAsset(rel); !allowed {
			t.Fatalf("%s must reach the execution machine", rel)
		}
	}
}

// kiroFixtureLogin is a kiro-cli database with a login, another auth row and
// a conversation, in AGENTFLOW_KIRO_DATA_DIR.
func kiroFixtureLogin(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "data.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE migrations (id INTEGER PRIMARY KEY, version INTEGER)`,
		`CREATE TABLE conversations (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO auth_kv VALUES ('kirocli:odic:token','{"refresh_token":"r"}'), ('kirocli:odic:device-registration','{}'), ('kirocli:auth:last-forced-refresh-at','1')`,
		`INSERT INTO migrations VALUES (1, 1)`,
		`CREATE TABLE state (key TEXT PRIMARY KEY, value BLOB)`,
		`INSERT INTO state VALUES ('api.codewhisperer.profile','{}'), ('telemetryUserId','u')`,
		`INSERT INTO conversations VALUES ('/home/me/project','secret history')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(kiroDataDirEnv, dir)
}

// A Docker sandbox holds no login: it runs on a copy of only the login, with
// HOME the private home, stale session leases removed and the token
// refreshed before the prompt.
func TestApplyKiroLoginDockerCopiesOnlyTheLogin(t *testing.T) {
	kiroFixtureLogin(t)
	if !KiroLoginAvailable() {
		t.Fatal("fixture login not found")
	}
	root := t.TempDir()
	argv, env, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Docker: true}, HostAgentCLIHome(filepath.Join(root, "home")), "/workspace/home", []string{"kiro-cli", "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != "/workspace/home" || env["KIRO_HOME"] != "/workspace/home/.kiro" || argv[0] != "sh" || !strings.Contains(argv[2], "session-leases") || !strings.Contains(argv[2], "--list-models") || argv[4] != "kiro-cli" {
		t.Fatalf("argv=%q env=%v", argv, env)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "home", kiroCopyDataRel))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var logins, others, conversations, migrations, states int
	_ = db.QueryRow(`SELECT count(*) FROM state WHERE key = 'api.codewhisperer.profile'`).Scan(&states)
	_ = db.QueryRow(`SELECT count(*) FROM auth_kv WHERE key LIKE 'kirocli:odic:%'`).Scan(&logins)
	_ = db.QueryRow(`SELECT count(*) FROM auth_kv WHERE key NOT LIKE 'kirocli:odic:%'`).Scan(&others)
	_ = db.QueryRow(`SELECT count(*) FROM conversations`).Scan(&conversations)
	_ = db.QueryRow(`SELECT count(*) FROM migrations`).Scan(&migrations)
	if logins != 2 || others != 0 || conversations != 0 || migrations != 1 || states != 1 {
		t.Fatalf("logins=%d others=%d conversations=%d migrations=%d profile=%d", logins, others, conversations, migrations, states)
	}
	t.Setenv(kiroDataDirEnv, t.TempDir())
	if _, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Docker: true}, HostAgentCLIHome(filepath.Join(root, "h2")), "/h", []string{"kiro-cli"}); err == nil || !strings.Contains(err.Error(), "kiro-cli login") {
		t.Fatalf("missing login must say how to log in: %v", err)
	}
}

// No sandbox gets write access to the machine's kiro-cli data directory: a
// process sandbox runs on a login copy in the private HOME, written where
// this OS's kiro-cli looks for it.
func TestApplyKiroLoginProcessSandboxUsesHostOSCopy(t *testing.T) {
	kiroFixtureLogin(t)
	defer func(previous func(context.Context) (string, error)) { ensureKiroHostRuntime = previous }(ensureKiroHostRuntime)
	ensureKiroHostRuntime = func(context.Context) (string, error) { return "/cache/kiro-runtime", nil }
	if len(ProviderRequirements(AgentCLIKiro).CLIStates) != 0 {
		t.Fatal("Kiro must not need the machine's login directory in a sandbox")
	}
	root := t.TempDir()
	_, env, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Sandboxed: true}, HostAgentCLIHome(filepath.Join(root, "home")), "/h", []string{"kiro-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != "/h" || env["XDG_DATA_HOME"] != "/h/.local/share" || env["AGENTFLOW_KIRO_RUNTIME"] != "/cache/kiro-runtime" {
		t.Fatalf("env = %v", env)
	}
	if _, err := os.Stat(filepath.Join(root, "home", kiroHostDataRel())); err != nil {
		t.Fatalf("login copy missing: %v", err)
	}
}

// The wrapper is real shell: sh must parse it, the installer's heredoc
// included.
func TestApplyKiroLoginDockerScriptParses(t *testing.T) {
	kiroFixtureLogin(t)
	argv, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Docker: true}, HostAgentCLIHome(filepath.Join(t.TempDir(), "home")), "/h", []string{"kiro-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", "-c", argv[2]).CombinedOutput(); err != nil {
		t.Fatalf("sh rejects the wrapper: %v %s", err, out)
	}
}

// A sandbox whose image lacks kiro-cli runs the machine's cached copy from
// the read-only RuntimeCLIDir mount instead of downloading its own.
func TestApplyKiroLoginDockerUsesCachedCLI(t *testing.T) {
	kiroFixtureLogin(t)
	root := t.TempDir()
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "")
	cached := kiroCachedCLI(root)
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv, env, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Docker: true}, HostAgentCLIHome(filepath.Join(t.TempDir(), "home")), "/h", []string{"kiro-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if env["AGENTFLOW_KIRO_CLI"] != cached || !strings.Contains(argv[2], "AGENTFLOW_KIRO_CLI") || env["AGENTFLOW_KIRO_RUNTIME"] != "/workspace/.agentflow/kiro-runtime" {
		t.Fatalf("argv=%q env=%v", argv, env)
	}
	// The published image bundles kiro-cli; its platform never downloads one.
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "ghcr.io/tencent/loopforge:x")
	_, env, _ = ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Docker: true}, HostAgentCLIHome(filepath.Join(t.TempDir(), "home")), "/h", []string{"kiro-cli"})
	if env["AGENTFLOW_KIRO_CLI"] != "" {
		t.Fatalf("env=%v", env)
	}
}

// A conversation workspace links the machine's cached kiro-cli; the
// in-container download is only the fallback.
func TestConversationKiroSetupFallsBackToDownload(t *testing.T) {
	setup, err := dockerCLISetup(AgentCLIKiro, "kiro-cli")
	if err != nil || !strings.Contains(setup, kiroInstallScript("/opt/loopforge/conversation-cli/bin")) {
		t.Fatalf("setup must carry the installer the cache replaces: %v", err)
	}
}

// A workspace turn may write KIRO_HOME; hooks, permission rules or steering
// it planted must not survive into the next conversation start.
func TestKiroConversationApprovalResetsPlantedConfig(t *testing.T) {
	root := t.TempDir()
	kiro := filepath.Join(root, "home", ".kiro")
	for _, rel := range []string{"hooks/evil.json", "settings/permissions.yaml", "settings/mcp.json", "steering/x.md", "sessions/s.json", "agents/role-a.md"} {
		path := filepath.Join(kiro, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InstallConversationApproval(root, AgentCLIKiro, CommandSpec{Argv: []string{"kiro-cli"}}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{"hooks/evil.json": false, "settings/permissions.yaml": false, "steering/x.md": false, "settings/mcp.json": true, "sessions/s.json": true, "agents/role-a.md": true, "hooks/loopforge-approval.json": true} {
		if _, err := os.Stat(filepath.Join(kiro, rel)); (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", rel, err == nil, want)
		}
	}
}

// A conversation keeps HOME across turns, so outside Docker nothing under it
// may shadow the trusted PATH or the tools the wrapper runs.
func TestKiroWrapperNeverRunsFromHome(t *testing.T) {
	kiroFixtureLogin(t)
	defer func(previous func(context.Context) (string, error)) { ensureKiroHostRuntime = previous }(ensureKiroHostRuntime)
	ensureKiroHostRuntime = func(context.Context) (string, error) { return "", errors.New("none") }
	argv, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{Sandboxed: true}, HostAgentCLIHome(t.TempDir()), "/h", []string{"kiro-cli"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(argv[2], "$HOME/.local/bin") || !strings.Contains(argv[2], "/bin/rm ") {
		t.Fatalf("script = %s", argv[2])
	}
}

// Every Kiro run sees all MCP tools directly: tool search stays off.
func TestKiroDisablesToolSearch(t *testing.T) {
	root := t.TempDir()
	if _, _, err := ApplyAgentCLICredential(AgentCLIKiro, ProviderCredential{}, AgentCLIOptions{}, HostAgentCLIHome(root), "/h", []string{"kiro-cli"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".kiro", "settings", "cli.json"))
	if err != nil || !strings.Contains(string(data), `"toolSearch.enabled":false`) {
		t.Fatalf("cli.json = %s, %v", data, err)
	}
}
