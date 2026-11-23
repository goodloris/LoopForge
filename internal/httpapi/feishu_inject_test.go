package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// feishuWorkspaceStart binds one workspace turn of user on an execution
// machine of runnerVersion and returns what travels to that machine.
func feishuWorkspaceStart(t *testing.T, h *assistantHandlers, user domain.User, runnerVersion string) *runtime.ConversationCommand {
	t.Helper()
	runnerID := "feishu-runner-" + strings.ReplaceAll(runnerVersion, ".", "-")
	if err := h.repo.PutRunner(domain.Runner{ID: runnerID, OwnerUserID: user.ID, Status: domain.RunnerStatusOnline, RunnerVersion: runnerVersion}); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{ChatOwnerID: user.ID, UserID: user.ID, User: user, ExecutionRunnerID: runnerID, RuntimeMode: domain.AssistantModeWorkspace}
	command, credentials := h.bindWorkspaceFeishu(context.Background(), s, runtime.CommandSpec{Argv: []string{"codebuddy"}, Provider: "codebuddy"}, runtime.RunCredentials{Feishu: runtime.FeishuCredential{UserToken: "stale-from-previous-speaker"}})
	return runtime.ConversationCommandFor(command, credentials)
}

// Both per-turn bindings sit on the workspace path of conversationExecutor:
// a shared conversation re-binds Git and Feishu for every speaker.
func TestWorkspaceTurnBindsFeishuBesideGit(t *testing.T) {
	source, err := os.ReadFile("assistant_process.go")
	if err != nil {
		t.Fatal(err)
	}
	want := "command, credentials = h.bindWorkspaceGit(ctx, s, command, credentials)\n\t\t\tcommand, credentials = h.bindWorkspaceFeishu(ctx, s, command, credentials)"
	if !strings.Contains(string(source), want) {
		t.Fatal("assistant_process.go must bind Feishu right after Git on the workspace path")
	}
	prompt, err := os.ReadFile("assistant.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), `"。\n" + h.feishuWorkspaceNote(toolSession.User.ID)`) {
		t.Fatal("the workspace prompt must carry the speaker's Feishu note")
	}
}

func feishuInjectFixture(t *testing.T) (*assistantHandlers, domain.User, *fakeFeishu) {
	t.Helper()
	fake := newFakeFeishu(t)
	h, user := workbenchFixture(t)
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "app-id")
	t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "app-secret")
	t.Setenv("LOOPFORGE_FEISHU_BASE_URL", fake.server.URL)
	t.Setenv("LOOPFORGE_FEISHU_ALLOWED_TENANTS", "tenantA")
	resetFeishuTenantTokenCache()
	return h, user, fake
}

func resetFeishuTenantTokenCache() {
	feishuTenantTokens.Lock()
	feishuTenantTokens.entries = map[string]feishuTenantTokenEntry{}
	feishuTenantTokens.Unlock()
}

func saveFeishuGrantFor(t *testing.T, s *repo.Store, userID int64, preset string) {
	t.Helper()
	if err := s.SaveFeishuGrant(userID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Name: "张三", Preset: preset, Tokens: repo.FeishuTokens{
		AccessToken: "u-speaker-token", AccessExpiresAt: time.Now().Add(2 * time.Hour), RefreshToken: "r-speaker", RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceTurnCarriesSpeakersFeishuIdentity(t *testing.T) {
	h, user, _ := feishuInjectFixture(t)
	saveFeishuGrantFor(t, h.repo, user.ID, "readonly")
	command := feishuWorkspaceStart(t, h, user, domain.RunnerVersionFeishu)
	feishu := command.Credentials.Feishu
	if !slices.Contains(command.Bindings, runtime.BindFeishu) || feishu.AppID != "app-id" || feishu.TenantToken != "t-xyz" || feishu.UserToken != "u-speaker-token" {
		t.Fatalf("start command = bindings %v feishu %+v", command.Bindings, feishu)
	}
	raw, _ := json.Marshal(command)
	if strings.Contains(string(raw), "app-secret") || strings.Contains(string(raw), "r-speaker") {
		t.Fatalf("the app secret or refresh token travelled with the command: %s", raw)
	}
}

func TestWorkspaceTurnWithoutConnectionHasBotOnly(t *testing.T) {
	h, user, _ := feishuInjectFixture(t)
	command := feishuWorkspaceStart(t, h, user, domain.RunnerVersionFeishu)
	if feishu := command.Credentials.Feishu; !slices.Contains(command.Bindings, runtime.BindFeishu) || feishu.TenantToken != "t-xyz" || feishu.UserToken != "" {
		t.Fatalf("not connected = %v %+v", command.Bindings, feishu)
	}
}

func TestWorkspaceTurnFeishuNeedsPlatformAppAndNewRunner(t *testing.T) {
	h, user, fake := feishuInjectFixture(t)
	saveFeishuGrantFor(t, h.repo, user.ID, "readonly")
	// An older execution machine would reject the binding: none is sent.
	if command := feishuWorkspaceStart(t, h, user, domain.RunnerVersionCodeBuddyBaseURL); slices.Contains(command.Bindings, runtime.BindFeishu) || command.Credentials.Feishu.AppID != "" {
		t.Fatalf("old runner got %v %+v", command.Bindings, command.Credentials.Feishu)
	}
	// Without a platform Feishu app nothing is injected or fetched, and a
	// previous speaker's token never carries over.
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
	calls := fake.tenantCalls
	if command := feishuWorkspaceStart(t, h, user, domain.RunnerVersionFeishu); slices.Contains(command.Bindings, runtime.BindFeishu) || command.Credentials.Feishu != (runtime.FeishuCredential{}) {
		t.Fatalf("unconfigured platform got %v %+v", command.Bindings, command.Credentials.Feishu)
	}
	if fake.tenantCalls != calls {
		t.Fatal("an unconfigured platform must not call Feishu")
	}
}

func TestFeishuRunCredentialNotices(t *testing.T) {
	h, user, fake := feishuInjectFixture(t)
	ctx := context.Background()
	if got := h.feishuRunCredential(ctx, user.ID); got.UserToken != "" || got.Notice != feishuNoticeNotConnected || got.TenantToken != "t-xyz" {
		t.Fatalf("not connected = %+v", got)
	}
	// Due for refresh and Feishu rejects it: expired, bot only.
	if err := h.repo.SaveFeishuGrant(user.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Tokens: repo.FeishuTokens{
		AccessToken: "u-old", AccessExpiresAt: time.Now().Add(time.Minute), RefreshToken: "r-old", RefreshExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	fake.refreshCode = 20037
	if got := h.feishuRunCredential(ctx, user.ID); got.UserToken != "" || got.Notice != feishuNoticeExpired {
		t.Fatalf("expired = %+v", got)
	}
	// A Feishu outage keeps the grant but this process gets the bot only.
	if err := h.repo.SaveFeishuGrant(user.ID, repo.FeishuGrantInput{Tenant: "tenantA", Subject: "union-1", Tokens: repo.FeishuTokens{
		AccessToken: "u-old", AccessExpiresAt: time.Now().Add(time.Minute), RefreshToken: "r-old", RefreshExpiresAt: time.Now().Add(time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	fake.refreshCode = 20050
	if got := h.feishuRunCredential(ctx, user.ID); got.UserToken != "" || got.Notice != feishuNoticeUnavailable {
		t.Fatalf("outage = %+v", got)
	}
	if g, _ := h.repo.FeishuGrant(user.ID); g.Status != repo.FeishuGrantActive {
		t.Fatalf("outage changed the grant to %q", g.Status)
	}
	t.Setenv("LOOPFORGE_FEISHU_APP_SECRET", "")
	if got := h.feishuRunCredential(ctx, user.ID); got != (runtime.FeishuCredential{}) {
		t.Fatalf("unconfigured = %+v", got)
	}
}

func TestFeishuWorkspaceNote(t *testing.T) {
	h, user, _ := feishuInjectFixture(t)
	if note := h.feishuWorkspaceNote(user.ID); !strings.Contains(note, "只有 bot 身份") || !strings.Contains(note, "个人配置 → 飞书") {
		t.Fatalf("not connected note = %q", note)
	}
	saveFeishuGrantFor(t, h.repo, user.ID, "readwrite")
	if note := h.feishuWorkspaceNote(user.ID); !strings.Contains(note, "用户身份为 张三（权限：读写）") || !strings.Contains(note, "不要运行 lark-cli config init") {
		t.Fatalf("connected note = %q", note)
	}
	t.Setenv("LOOPFORGE_FEISHU_APP_ID", "")
	if note := h.feishuWorkspaceNote(user.ID); note != "" {
		t.Fatalf("unconfigured note = %q", note)
	}
}

func TestDiagnosticsRedactFeishuTokens(t *testing.T) {
	for _, token := range []string{"u-7Hk2mQ9xLp4Rz8Vb1Nc6Td3Ws5Ya0Ef", "t-g1044Nkcd3c2AbCdEfGhIjKlMnOpQrSt", "eyJhbGciOiJFUzI1NiIs.eyJzdWIiOiJvdV8xMjM0NTY3OCJ9.c2lnbmF0dXJl"} {
		out := sanitizeAssistantDiagnostic("lark-cli failed with "+token+" in output", domain.User{}, 1024)
		if strings.Contains(out, token) {
			t.Fatalf("token survived: %q", out)
		}
	}
	if out := sanitizeAssistantDiagnostic("LARKSUITE_CLI_USER_ACCESS_TOKEN=abc123 set", domain.User{}, 1024); strings.Contains(out, "abc123") {
		t.Fatalf("env assignment survived: %q", out)
	}
}

// The engine asks the HTTP layer (which owns the Feishu app) for an SOP's
// responsible user's tokens.
func TestServerWiresFeishuCredential(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "e.FeishuCredential = d.feishuRunCredential") {
		t.Fatal("server.go must hand the engine the Feishu credential source")
	}
}
