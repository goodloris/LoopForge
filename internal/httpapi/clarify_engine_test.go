package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// Clarification checks the credentials of the CLI it runs on: CodeBuddy and
// the other CLIs the responsible user's key, Kiro CLI kiro-cli and its login
// on this server.
func TestClarifyEngineRefusal(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	none := domain.User{ID: 7}
	keyed := domain.User{ID: 8, CodeBuddyAPIKey: "cb", CodexAPIKey: "cx"}
	clarifyEngineRefusal := func(u domain.User, model string) error {
		_, err := clarifyModelFor(model, u)
		return err
	}
	modelFor := func(id string, u domain.User) string {
		model, err := clarifyModelFor(id, u)
		if err != nil {
			t.Fatalf("clarifyModelFor(%q) = %v", id, err)
		}
		return model
	}
	if err := clarifyEngineRefusal(none, "glm-5.1"); err != errClarifyResponsibleKey {
		t.Fatalf("CodeBuddy without key = %v", err)
	}
	if err := clarifyEngineRefusal(keyed, "glm-5.1"); err != nil {
		t.Fatalf("CodeBuddy with key = %v", err)
	}
	if err := clarifyEngineRefusal(keyed, "kiro:auto"); err != errClarifyKiroCLI || !strings.Contains(err.Error(), "kiro-cli login") {
		t.Fatalf("Kiro without kiro-cli = %v", err)
	}
	if err := clarifyEngineRefusal(none, "codex:gpt-5.5"); err == nil || !strings.Contains(err.Error(), "Codex API Key") {
		t.Fatalf("Codex without key = %v", err)
	}
	if err := clarifyEngineRefusal(keyed, "codex:gpt-5.5"); err != nil {
		t.Fatalf("Codex with key = %v", err)
	}
	kiroCLIInstalled = func() bool { return true }
	if err := clarifyEngineRefusal(none, "kiro:auto"); err != nil {
		t.Fatalf("Kiro needs no key: %v", err)
	}
	if err := clarifyEngineRefusal(none, "kiro:bad model"); err == nil {
		t.Fatal("invalid Kiro model accepted")
	}
	// No choice: CodeBuddy with a key, else Kiro CLI when it can run here.
	if got := modelFor("", keyed); got != featureDefaultModel(keyed, assistantEngineCodeBuddy) {
		t.Fatalf("default with CodeBuddy key = %q", got)
	}
	if got := modelFor("", none); got != "kiro:auto" {
		t.Fatalf("default without CodeBuddy key = %q", got)
	}
	if got := modelFor("kiro:claude-haiku-5.5", keyed); got != "kiro:claude-haiku-5.5" {
		t.Fatalf("Kiro choice = %q", got)
	}
}

// A Kiro CLI clarification without kiro-cli on the server is refused before
// any job starts, with what to do.
func TestChatClarifyRefusesKiroWithoutCLI(t *testing.T) {
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return false }
	called := false
	setClarifyExecForTest(func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		called = true
		return map[string]any{}, "{}", map[string]any{}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "model": "kiro:auto"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kiro-cli login") {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("clarification started without kiro-cli")
	}
}

// CodeBuddy keeps refusing a responsible user without a CodeBuddy key before
// any session or checkout.
func TestClarifyRefusesResponsibleWithoutCodeBuddyKey(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.PutUser(domain.User{ID: 7, LoginName: "kiro-only"})
	st.PutProject(domain.Project{ID: "kiro-p", OwnerID: 7})
	h := &streamHandlers{deps: &deps{repo: st}}
	_, _, _, err = h.executeClarify(context.Background(), domain.Issue{ProjectID: "kiro-p", Title: "x"}, chatClarifyRequest{Message: "加个徽章", Model: "glm-5.1"}, nil)
	if err != errClarifyResponsibleKey || !strings.Contains(err.Error(), "CodeBuddy API Key") {
		t.Fatalf("err = %v", err)
	}
}

// fakeKiroClarifier is a kiro-cli that records its argv and working
// directory, submits a clarification through the platform MCP server named
// in its KIRO_HOME, and finishes the run.
func fakeKiroClarifier(t *testing.T, submission map[string]any) (argvFile, cwdFile, policyFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile, cwdFile, policyFile = filepath.Join(dir, "argv"), filepath.Join(dir, "cwd"), filepath.Join(dir, "permissions.yaml")
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": submitClarificationTool, "arguments": submission}})
	if err := os.WriteFile(filepath.Join(dir, "call.json"), call, 0o600); err != nil {
		t.Fatal(err)
	}
	// The platform stops the CLI once the clarification is submitted, so
	// the read is streamed before the submission.
	before := `{"type":"runStarted","data":{"payloadSchema":"acp","engine":"v3"}}
{"type":"sessionUpdate","data":{"sessionId":"sess_fake","update":{"sessionUpdate":"tool_call","toolCallId":"read_toolu_1","title":"read","status":"completed","rawInput":{"file_path":"main.go"}}}}
`
	after := `{"type":"runFinished","data":{"sessionId":"sess_fake","status":"success","stopReason":"end_turn","finalText":"已提交"}}
`
	for name, data := range map[string]string{"before": before, "after": after} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > ` + argvFile + `
pwd > ` + cwdFile + `
cp "$KIRO_HOME/settings/permissions.yaml" ` + policyFile + `
cat ` + filepath.Join(dir, "before") + `
python3 - "$KIRO_HOME/settings/mcp.json" ` + filepath.Join(dir, "call.json") + ` <<'PY' || exit 1
import json, sys, urllib.request
url = json.load(open(sys.argv[1]))["mcpServers"]["agentflow_session"]["url"]
def post(body):
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    return urllib.request.urlopen(req).read().decode()
post(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}}).encode())
out = post(open(sys.argv[2], "rb").read())
if "提交成功" not in out:
    sys.exit("submit failed: " + out)
PY
# The platform stops the CLI once the clarification is submitted.
sleep 5
cat ` + filepath.Join(dir, "after") + "\n"
	bin := filepath.Join(dir, "kiro-cli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_KIRO_BIN", bin)
	t.Setenv("AGENTFLOW_KIRO_DATA_DIR", "")
	return argvFile, cwdFile, policyFile
}

// A Kiro-only responsible user clarifies on Kiro CLI: it runs in the
// repository, trusts only the file read tools and the platform MCP server,
// returns the clarification it submitted through MCP, and the next round
// resumes the same Kiro session.
func TestClarifyRunsOnKiroCLI(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs /bin/sh")
	}
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return true }
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 71, LoginName: "kiro-only"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "kiro-src", ProjectID: "platform", Name: "src", LocalPath: source}); err != nil {
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

	argvFile, cwdFile, policyFile := fakeKiroClarifier(t, map[string]any{
		"summary": "徽章位置未定", "ready": false,
		"questions": []any{map[string]any{"id": "q1", "question": "徽章放在列表页还是详情页？", "options": []any{"列表页", "详情页"}, "multiSelect": false}},
	})
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{ProjectID: "platform", AssigneeUserID: user.ID, Title: "加徽章", Description: "需求列表加徽章", RepositoryIDs: []string{"kiro-src"}}
	var tools []string
	emit := func(event string, payload any) {
		if m, ok := payload.(map[string]string); ok && m["kind"] == "tool" {
			tools = append(tools, m["tool"])
		}
	}
	parsed, _, info, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{Message: "加个徽章", Model: "kiro:claude-haiku-5.5"}, emit)
	if err != nil {
		t.Fatal(err)
	}
	result := parseClarifyModel(parsed, "")
	if err := result.userFacingOK(); err != nil || len(result.Questions) != 1 || result.Questions[0].Question != "徽章放在列表页还是详情页？" {
		t.Fatalf("clarification = %#v (%v)", parsed, err)
	}
	if info["analyzed"] != true {
		t.Fatalf("repository not analyzed: %#v", info)
	}
	if cwd, _ := os.ReadFile(cwdFile); !sameDir(t, strings.TrimSpace(string(cwd)), source) {
		t.Fatalf("kiro-cli ran in %q, want the checkout %q", cwd, source)
	}
	argv := readFakeArgv(t, argvFile)
	if trust := kiroTrust(t, argv); trust != "read,grep,glob,@"+agentToolSessionMCPServerID {
		t.Fatalf("clarification must trust only the read tools and the platform MCP: %q", trust)
	}
	if !slices.Contains(argv, "claude-haiku-5.5") || slices.Contains(argv, "--trust-all-tools") {
		t.Fatalf("argv = %q", argv)
	}
	// Reads outside the checkout are denied by Kiro's permission policy.
	real, _ := filepath.EvalSymlinks(source)
	if policy, err := os.ReadFile(policyFile); err != nil || !strings.Contains(string(policy), `"effect":"deny","exclude":["`+real+`","`+real+`/**"],"match":["/**"]`) {
		t.Fatalf("read policy = %s (%v)", policy, err)
	}
	if !slices.Contains(tools, "read") && !slices.Contains(tools, "Read") {
		t.Fatalf("tool progress not forwarded: %q", tools)
	}

	// The answer round resumes the recorded Kiro session.
	sessionID, _ := info["sessionId"].(string)
	next := chatClarifyRequest{Message: "列表页", Model: "kiro:claude-haiku-5.5", SessionID: sessionID}
	next.History = append(next.History, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "assistant", Content: "徽章放在列表页还是详情页？"})
	issue.Clarification.SessionUserID = user.ID
	// Every answer round resumes the recorded Kiro session, also after a
	// resumed round that the submission stopped.
	for round := 2; round <= 3; round++ {
		if err := os.Remove(argvFile); err != nil {
			t.Fatal(err)
		}
		_, _, info, err = h.executeClarify(context.Background(), issue, next, nil)
		if err != nil {
			t.Fatal(err)
		}
		argv = readFakeArgv(t, argvFile)
		if i := slices.Index(argv, "--resume-id"); i < 0 || argv[i+1] != "sess_fake" {
			t.Fatalf("round %d did not resume the Kiro session: %q", round, argv)
		}
		next.SessionID, _ = info["sessionId"].(string)
	}
}

// sameDir compares directories through symlinks (macOS /var → /private/var).
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errors.Join(errA, errB) == nil && ra == rb
}

// The CodeBuddy read guard allows file tools inside the read root only,
// through symlinks and relative patterns, and refuses what it cannot parse.
func TestClarifyReadGuard(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "checkout")
	outside := filepath.Join(dir, "secret.txt")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "guard.py")
	if err := os.WriteFile(script, []byte(clarifyReadGuard), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(event string) bool {
		cmd := exec.Command(python, "-I", script, root)
		cmd.Stdin = strings.NewReader(event)
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), `"deny"`) {
			t.Fatalf("guard crashed on %s: %v %s", event, err, out)
		}
		return err == nil
	}
	event := func(name string, input map[string]any) string {
		data, _ := json.Marshal(map[string]any{"tool_name": name, "tool_input": input, "cwd": root})
		return string(data)
	}
	for _, allowed := range []string{
		event("Read", map[string]any{"file_path": filepath.Join(root, "src", "main.go")}),
		event("Read", map[string]any{"file_path": "src/main.go"}),
		event("Grep", map[string]any{"pattern": "/etc/passwd", "glob": "*.go"}),
		event("Glob", map[string]any{"pattern": "**/*.go"}),
		event("Glob", map[string]any{"pattern": root + "/**/*.go"}),
		event("mcp__agentflow_session__submit_clarification", map[string]any{"path": "/etc"}),
	} {
		if !run(allowed) {
			t.Fatalf("refused %s", allowed)
		}
	}
	for _, denied := range []string{
		event("Read", map[string]any{"file_path": outside}),
		event("Read", map[string]any{"file_path": "/proc/1/environ"}),
		event("Read", map[string]any{"file_path": "link.txt"}),
		event("Read", map[string]any{"file_path": "../secret.txt"}),
		event("Grep", map[string]any{"pattern": "x", "path": dir}),
		event("Grep", map[string]any{"pattern": "x", "glob": "../*"}),
		event("Glob", map[string]any{"pattern": "/etc/*"}),
		event("Glob", map[string]any{"pattern": "../**"}),
		event("Glob", map[string]any{"pattern": "~/.ssh/*"}),
		event("LS", map[string]any{"path": "/"}),
		`not json`,
	} {
		if run(denied) {
			t.Fatalf("allowed %s", denied)
		}
	}
}

// A CodeBuddy clarification gets the read guard in its settings, confined
// to the checkout.
func TestClarifyCodeBuddyGetsReadGuard(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTFLOW_CLARIFY_SESSION_DIR", filepath.Join(root, "sessions"))
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user := domain.User{ID: 73, LoginName: "cb", CodeBuddyAPIKey: "cb-key"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "cb-src", ProjectID: "platform", Name: "src", LocalPath: source}); err != nil {
		t.Fatal(err)
	}
	settingsCopy := filepath.Join(root, "settings.json")
	bin := filepath.Join(root, "codebuddy")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do [ "$1" = --settings ] && cp "$2" ` + settingsCopy + `; shift; done
printf '%s\n' '{"type":"result","result":"{\"summary\":\"ok\",\"ready\":true,\"questions\":[]}"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	h := &streamHandlers{deps: &deps{repo: st}}
	issue := domain.Issue{ProjectID: "platform", AssigneeUserID: user.ID, Title: "x", Description: "y", RepositoryIDs: []string{"cb-src"}}
	if _, _, _, err := h.executeClarify(context.Background(), issue, chatClarifyRequest{Message: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsCopy)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Permissions struct{ Deny []string }
		Hooks       struct {
			PreToolUse []struct {
				Matcher string
				Hooks   []struct{ Command, OnFailure string }
			}
		}
	}
	if err := json.Unmarshal(data, &settings); err != nil || len(settings.Hooks.PreToolUse) != 1 || len(settings.Hooks.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("settings = %s (%v)", data, err)
	}
	hook := settings.Hooks.PreToolUse[0].Hooks[0]
	real, _ := filepath.EvalSymlinks(source)
	if !strings.Contains(hook.Command, "clarify-read-guard.py") || !strings.HasSuffix(hook.Command, " '"+real+"'") || hook.OnFailure != "block" {
		t.Fatalf("hook = %+v", hook)
	}
	if !slices.Contains(settings.Permissions.Deny, "Bash") {
		t.Fatalf("native tool denials lost: %s", data)
	}
}

func TestChatClarifyRejectsInvalidModel(t *testing.T) {
	called := false
	setClarifyExecForTest(func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		called = true
		return map[string]any{}, "{}", map[string]any{}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "model": "codex:"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请选择有效的 CLI 模型") {
		t.Fatalf("error does not explain invalid model: %s", rec.Body.String())
	}
	if called {
		t.Fatal("clarification started with an invalid model")
	}
}

func TestRunEvalRequiresSelectedCLICredentials(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "eval-key"}); err != nil {
		t.Fatal(err)
	}
	done := domain.Run{ID: "eval-codex-run", Status: "succeeded", WorkflowID: "missing",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(done); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/runs/eval-codex-run/eval", map[string]any{"model": "codex:gpt-5.5"}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Codex API Key") {
		t.Fatalf("status = %d body = %s, want 400 asking for the selected CLI credential", rec.Code, rec.Body.String())
	}
	if _, busy := f.s.ActiveRunEval(done.ID); busy {
		t.Fatal("a model without matching credentials must not start an eval")
	}
}
