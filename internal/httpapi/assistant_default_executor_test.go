package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Without AGENTFLOW_DEFAULT_EXECUTOR every default stays CodeBuddy and an
// empty request is passed on unchanged.
func TestAssistantDefaultExecutorUnsetKeepsCodeBuddy(t *testing.T) {
	t.Setenv(assistantDefaultExecutorEnv, "")
	if got := assistantDefaultExecutor(); got != assistantEngineCodeBuddy {
		t.Fatalf("default = %q", got)
	}
	if got := assistantExecutor(""); got != assistantEngineCodeBuddy {
		t.Fatalf("assistantExecutor(\"\") = %q", got)
	}
	if got := assistantDefaultRequestModel("", "", true); got != "" {
		t.Fatalf("empty request model = %q", got)
	}
	if got := assistantRequestModel("", assistantSponsoredModel); got != assistantSponsoredModel {
		t.Fatalf("CodeBuddy model = %q", got)
	}
}

func TestAssistantDefaultExecutorOverride(t *testing.T) {
	for raw, want := range map[string]string{
		"kiro":      runtime.AgentCLIKiro,
		" KIRO ":    runtime.AgentCLIKiro,
		"claude":    assistantEngineClaude,
		"codex":     assistantEngineCodex,
		"codebuddy": assistantEngineCodeBuddy,
		"opencode":  "opencode",
	} {
		t.Setenv(assistantDefaultExecutorEnv, raw)
		if got := assistantDefaultExecutor(); got != want {
			t.Errorf("%q: default = %q, want %q", raw, got, want)
		}
	}
	t.Setenv(assistantDefaultExecutorEnv, "kiro")
	if got := assistantExecutor(""); got != runtime.AgentCLIKiro {
		t.Fatalf("assistantExecutor(\"\") = %q", got)
	}
	if got := assistantExecutor("codex"); got != "codex" {
		t.Fatalf("named executor must win: %q", got)
	}
	if got := assistantDefaultRequestModel("", "", true); got != "kiro:auto" {
		t.Fatalf("empty request model = %q", got)
	}
	// An existing conversation keeps its CLI: an old CodeBuddy conversation
	// sends no executor and must not be moved onto the default CLI (409).
	if got := assistantDefaultRequestModel("", "", false); got != "" {
		t.Fatalf("existing conversation re-targeted: %q", got)
	}
	if refusal := assistantEngineSwitchRefusal(assistantEngineCodeBuddy, assistantRequestModel("", assistantDefaultRequestModel("", "", false))); refusal != "" {
		t.Fatalf("old CodeBuddy conversation refused: %q", refusal)
	}
	// Anything the caller named is their choice.
	for _, tc := range []struct{ executor, model string }{
		{"", assistantSponsoredModel},
		{"codebuddy", ""},
		{"codex", ""},
		{"", "codex:gpt-5.5"},
	} {
		if got := assistantDefaultRequestModel(tc.executor, tc.model, true); got != tc.model {
			t.Errorf("%+v: model = %q", tc, got)
		}
	}
	// Helper turns pass CodeBuddy catalog IDs with no executor; the default
	// CLI must not re-prefix them.
	if got := assistantRequestModel("", assistantSponsoredModel); got != assistantSponsoredModel {
		t.Fatalf("CodeBuddy model re-prefixed: %q", got)
	}
	if got := assistantRequestModel("kiro", "default"); got != "kiro:default" {
		t.Fatalf("named executor prefix = %q", got)
	}
}

func TestAssistantDefaultExecutorInvalidFallsBackAndWarnsOnce(t *testing.T) {
	buf := captureLog(t)
	t.Setenv(assistantDefaultExecutorEnv, "foo-invalid-1")
	for range 3 {
		if got := assistantDefaultExecutor(); got != assistantEngineCodeBuddy {
			t.Fatalf("invalid value must fall back to codebuddy, got %q", got)
		}
	}
	if n := strings.Count(buf.String(), `"foo-invalid-1"`); n != 1 {
		t.Fatalf("want one warning, got %d: %s", n, buf.String())
	}
	if !strings.Contains(buf.String(), assistantDefaultExecutorEnv) || !strings.Contains(buf.String(), "kiro") {
		t.Fatalf("warning must name the variable and valid values: %s", buf.String())
	}
	t.Setenv(assistantDefaultExecutorEnv, "foo-invalid-2")
	assistantDefaultExecutor()
	if !strings.Contains(buf.String(), `"foo-invalid-2"`) {
		t.Fatalf("a second invalid value warns too: %s", buf.String())
	}
	if got := assistantDefaultRequestModel("", "", true); got != "" {
		t.Fatalf("invalid default must keep the CodeBuddy empty model: %q", got)
	}
}

func assistantModelsResponse(t *testing.T, h *assistantHandlers, viewer *domain.User, query string) (assistantModelCatalog, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/api/assistant/models"+query, nil)
	if viewer != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(*viewer)))
	}
	h.models(c)
	var catalog assistantModelCatalog
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &catalog) != nil {
		t.Fatalf("models%s: %d %s", query, rec.Code, rec.Body.String())
	}
	return catalog, rec.Body.String()
}

// /api/assistant/models announces the default CLI; nothing else in the
// catalog depends on it.
func TestAssistantModelsAnnouncesDefaultExecutor(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	stubCLIModelDiscovery(t)
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return true }
	if err := st.PutUser(domain.User{LoginName: "buddy", CodeBuddyAPIKey: "own-secret"}); err != nil {
		t.Fatal(err)
	}
	buddy, _ := st.UserByLoginName("buddy")
	h := &assistantHandlers{deps: &deps{repo: st}}

	for _, viewer := range []*domain.User{&buddy, nil} {
		t.Setenv(assistantDefaultExecutorEnv, "")
		base, _ := assistantModelsResponse(t, h, viewer, "")
		if base.DefaultExecutor != assistantEngineCodeBuddy {
			t.Fatalf("signed in %v: unset default = %q", viewer != nil, base.DefaultExecutor)
		}
		t.Setenv(assistantDefaultExecutorEnv, "kiro")
		kiro, _ := assistantModelsResponse(t, h, viewer, "")
		if kiro.DefaultExecutor != runtime.AgentCLIKiro {
			t.Fatalf("signed in %v: kiro default = %q", viewer != nil, kiro.DefaultExecutor)
		}
		kiro.DefaultExecutor = base.DefaultExecutor
		a, _ := json.Marshal(base)
		b, _ := json.Marshal(kiro)
		if !bytes.Equal(a, b) {
			t.Fatalf("signed in %v: catalog must not change with the default CLI:\n%s\n%s", viewer != nil, a, b)
		}
	}
	t.Setenv(assistantDefaultExecutorEnv, "kiro")
	if _, body := assistantModelsResponse(t, h, &buddy, "?executor=codex"); strings.Contains(body, "defaultExecutor") {
		t.Fatalf("executor catalogs carry no default: %s", body)
	}
}

// A refusal names the CLI the turn asked for; CodeBuddy keeps its wording.
func TestAssistantMissingCredentialNamesTheEngine(t *testing.T) {
	if got := assistantMissingCredential(assistantEngineCodeBuddy); got != "请先在个人配置中配置自己的 CodeBuddy API Key" {
		t.Fatalf("CodeBuddy wording changed: %q", got)
	}
	for _, engine := range []string{runtime.AgentCLIKiro, assistantEngineCodex, "opencode"} {
		got := assistantMissingCredential(engine)
		if strings.Contains(got, "CodeBuddy") || got != assistantEngineMissingKey(engine) {
			t.Errorf("%s: %q", engine, got)
		}
	}
	if !strings.Contains(assistantMissingCredential(runtime.AgentCLIKiro), "Kiro CLI") {
		t.Fatal("Kiro refusal must point at Kiro CLI")
	}
}

func TestAssistantEmptyTurnFollowsTheDefaultExecutor(t *testing.T) {
	st, newcomer := sponsorFixture(t)
	stubCLIModelDiscovery(t)
	h := &assistantHandlers{deps: &deps{repo: st}}

	t.Setenv(assistantDefaultExecutorEnv, "")
	if _, _, refusal := h.assistantTurnUser(newcomer, assistantDefaultRequestModel("", "", true), false); refusal != "请先在个人配置中配置自己的 CodeBuddy API Key" {
		t.Fatalf("unset default must keep the CodeBuddy refusal: %q", refusal)
	}
	t.Setenv(assistantDefaultExecutorEnv, "codex")
	if _, _, refusal := h.assistantTurnUser(newcomer, assistantDefaultRequestModel("", "", true), false); refusal != assistantEngineMissingKey(assistantEngineCodex) {
		t.Fatalf("codex default refusal = %q", refusal)
	}
	t.Setenv(assistantDefaultExecutorEnv, "kiro")
	_, model, refusal := h.assistantTurnUser(newcomer, assistantDefaultRequestModel("", "", true), false)
	if strings.Contains(refusal, "CodeBuddy") || model != "kiro:auto" {
		t.Fatalf("kiro default turn = %q / %q", model, refusal)
	}
	defer func(previous func() bool) { kiroCLIInstalled = previous }(kiroCLIInstalled)
	kiroCLIInstalled = func() bool { return true }
	if got := assistantAllowedUserModel(model, assistantModelCatalog{Default: assistantSponsoredModel}, domain.User{}); got != "kiro:auto" {
		t.Fatalf("kiro:auto must be accepted where Kiro runs: %q", got)
	}
}
