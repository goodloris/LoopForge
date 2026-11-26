package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestPutAgentValidatesEffortLevel(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "effort-role", Name: "Effort", Version: 1, ProjectID: "gslb", ExecutorID: "codex", Effort: "xhigh",
	}, http.StatusOK)
	if saved.Effort != "xhigh" {
		t.Fatalf("saved effort = %q, want xhigh", saved.Effort)
	}
	errBody := requestJSON[map[string]string](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "effort-role", Name: "Effort", Version: 2, ProjectID: "gslb", ExecutorID: "codex", Effort: "ultracode",
	}, http.StatusBadRequest)
	if !strings.Contains(errBody["error"], "思考强度") {
		t.Fatalf("error = %#v", errBody)
	}
}

func TestExecutorModelsCatalogListsPerCLI(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "fast-model,test-model")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	// The catalog is a public, non-sensitive list served without login.
	catalogs := requestEmptyJSON[map[string]executorModelCatalog](t, h, http.MethodGet, "/api/executor-models", http.StatusOK)
	cb, ok := catalogs["codebuddy"]
	if !ok || len(cb.Models) == 0 || cb.Default == "" {
		t.Fatalf("codebuddy catalog = %+v", cb)
	}
	if cb.Models[0].Credits != "" {
		t.Fatal("unconfigured public model must not claim a private price")
	}
	for _, model := range cb.Models {
		if model.ID == assistantDefaultModel {
			t.Fatal("the CodeBuddy CLI default must not be listed as a model")
		}
	}
	if cb.Models[0].ID != "fast-model" || cb.Models[0].Family != "codebuddy" {
		t.Fatalf("CodeBuddy routing model = %+v", cb.Models[0])
	}
	// An anonymous caller has no Codex / Claude Code credential, so the
	// built-in lists must not be presented as supported (state none). The
	// keys stay present with an empty array for the wizard cache.
	for _, key := range []string{"codex", "claude"} {
		catalog, ok := catalogs[key]
		if !ok || catalog.Models == nil || len(catalog.Models) != 0 || catalog.Source != "none" ||
			catalog.Availability != executorModelsNone || catalog.Notice == "" || !catalog.CustomModel || len(catalog.Efforts) == 0 {
			t.Fatalf("%s anonymous catalog = %+v", key, catalog)
		}
	}
	raw := requestEmptyJSON[map[string]json.RawMessage](t, h, http.MethodGet, "/api/executor-models", http.StatusOK)
	if !strings.Contains(string(raw["codex"]), `"models":[]`) || !strings.Contains(string(raw["claude"]), `"models":[]`) {
		t.Fatalf("empty catalogs must serialize models as []: codex=%s claude=%s", raw["codex"], raw["claude"])
	}
	// The full built-in catalog (order and names) stays on ?builtin=1.
	catalogs = requestEmptyJSON[map[string]executorModelCatalog](t, h, http.MethodGet, "/api/executor-models?builtin=1", http.StatusOK)
	codex := catalogs["codex"]
	if codex.Availability != "" || codex.Source != "" {
		t.Fatalf("builtin catalog must not carry availability: %+v", codex)
	}
	if len(codex.Models) < 2 || codex.Models[0].ID != "gpt-6.1-sol" || codex.Models[1].ID != "gpt-6.1-terra" || codex.Models[1].Name != "GPT-6.1 Terra" {
		t.Fatalf("codex catalog = %+v", codex.Models)
	}
	codexNames := make(map[string]string, len(codex.Models))
	for _, model := range codex.Models {
		codexNames[model.ID] = model.Name
	}
	for id, name := range map[string]string{"gpt-6.2-sol": "GPT-6.2 Sol", "gpt-6.2-astra": "GPT-6.2 Astra"} {
		if got := codexNames[id]; got != name {
			t.Errorf("Codex model %s has name %q, want %q", id, got, name)
		}
	}
	claude := catalogs["claude"]
	if len(claude.Models) < 2 || claude.Models[0].ID != "claude-fable-5-1" ||
		claude.Models[1].ID != "claude-fable-5-2" || claude.Models[1].Name != "Claude Fable 5.2" {
		t.Fatalf("claude catalog = %+v", claude.Models)
	}
	haiku := -1
	for i, model := range claude.Models {
		if model.ID == "claude-haiku-5-5" {
			haiku = i
		}
	}
	if haiku < 0 || haiku+1 >= len(claude.Models) || claude.Models[haiku].Name != "Claude Haiku 5.5" || claude.Models[haiku+1].ID != "claude-haiku-4-5" {
		t.Fatalf("claude catalog = %+v", claude.Models)
	}
}

func TestEnvironmentAssetsAreFilteredByProjectAndSharedScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha"}, {ID: "beta", Key: "BETA", Name: "Beta"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []domain.DockerfileDefinition{
		{ID: "alpha-dockerfile", Name: "Alpha", ProjectID: "alpha", Content: "FROM alpine"},
		{ID: "beta-shared-dockerfile", Name: "Beta shared", ProjectID: "beta", Shared: true, Content: "FROM alpine"},
		{ID: "beta-private-dockerfile", Name: "Beta private", ProjectID: "beta", Content: "FROM alpine"},
	} {
		if err := s.PutDockerfile(item); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	items := requestEmptyJSON[[]domain.DockerfileDefinition](t, h, http.MethodGet, "/api/dockerfiles?projectId=alpha", http.StatusOK)
	visible := map[string]bool{}
	for _, item := range items {
		visible[item.ID] = true
	}
	if !visible["alpha-dockerfile"] || !visible["beta-shared-dockerfile"] || visible["beta-private-dockerfile"] {
		t.Fatalf("unexpected project catalog visibility: %#v", visible)
	}
}

func TestWorkflowAgentsAreFilteredAndValidatedByProject(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "alpha", Key: "ALPHA", Name: "Alpha"}, {ID: "beta", Key: "BETA", Name: "Beta"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range []domain.AgentDefinition{
		{ID: "alpha-role", Name: "Alpha role", ProjectID: "alpha", Version: 1},
		{ID: "beta-shared-role", Name: "Beta shared", ProjectID: "beta", Shared: true, Version: 1},
		{ID: "beta-private-role", Name: "Beta private", ProjectID: "beta", Version: 1},
		{ID: "legacy-role", Name: "Legacy global", Version: 1},
	} {
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	agents := requestEmptyJSON[[]domain.AgentDefinition](t, h, http.MethodGet, "/api/agents?projectId=alpha", http.StatusOK)
	visible := map[string]bool{}
	for _, agent := range agents {
		visible[agent.ID] = true
	}
	if !visible["alpha-role"] || !visible["beta-shared-role"] || visible["beta-private-role"] || visible["legacy-role"] {
		t.Fatalf("unexpected workflow role visibility: %#v", visible)
	}

	invalid := domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "beta-private-role"}},
	}
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", invalid, http.StatusBadRequest)
	invalid.Nodes[0].AgentID = "beta-shared-role"
	saved := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", invalid, http.StatusOK)
	if saved.Nodes[0].AgentID != "beta-shared-role" {
		t.Fatalf("shared role was not saved: %+v", saved.Nodes)
	}
	legacyPayload := map[string]any{
		"id": "legacy-node-gate", "name": "Legacy node gate", "projectId": "alpha", "sandboxProfileId": "local",
		"nodes": []any{map[string]any{
			"id": "work", "name": "Work", "type": "agent", "agentId": "alpha-role",
			"verification": []any{map[string]any{"id": "obsolete", "mode": "command", "commands": []string{"false"}}},
		}},
	}
	stored := requestJSON[map[string]any](t, h, http.MethodPut, "/api/workflows", legacyPayload, http.StatusOK)
	nodes, _ := stored["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	if _, exists := node["verification"]; exists {
		t.Fatalf("workflow API retained removed node verification: %#v", node)
	}
}

func TestSharingSandboxCascadesDependenciesAndSharedWorkflowRequiresSharedSandbox(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDockerfile(domain.DockerfileDefinition{ID: "alpha-dockerfile", Name: "Alpha image", ProjectID: "alpha", Content: "FROM alpine"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvironment(domain.EnvironmentDefinition{ID: "alpha-tools", Name: "Alpha tools", ProjectID: "alpha", InstallScript: "true"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", domain.SandboxProfile{
		ID: "alpha-container", Name: "Alpha container", ProjectID: "alpha", Shared: true, Driver: "docker", DockerfileID: "alpha-dockerfile",
	}, http.StatusOK)
	dockerfile, _ := s.Dockerfile("alpha-dockerfile")
	if !dockerfile.Shared {
		t.Fatal("sharing a sandbox did not share its Dockerfile")
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", domain.SandboxProfile{
		ID: "alpha-process", Name: "Alpha process", ProjectID: "alpha", Driver: "bwrap", EnvironmentID: "alpha-tools",
	}, http.StatusOK)
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", Shared: true, SandboxProfileID: "alpha-process",
	}, http.StatusBadRequest)
	profile, _ := s.Sandbox("alpha-process")
	environment, _ := s.Environment("alpha-tools")
	if profile.Shared || environment.Shared {
		t.Fatalf("rejected workflow unexpectedly shared its environment chain: sandbox=%+v environment=%+v", profile, environment)
	}
}

func TestCatalogAgentsKeepsProjectScope(t *testing.T) {
	source := []domain.AgentDefinition{{
		ID: "gslb-fix", Name: "Fix", Version: 2, ProjectID: "gslb", Shared: true, PlatformMCP: true,
		Description:     strings.Repeat("x", 300),
		Prompt:          "fix",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md"}},
		Verification: []domain.VerificationGate{{
			ID: "plan", Name: "Plan", Mode: "command",
			Commands: []string{"test -s fix-plan.md"}, ExecutionScope: domain.ExecutionScopeRun,
		}},
	}}
	got := catalogAgents(source)
	if len(got) != 1 || got[0].ProjectID != "gslb" || !got[0].Shared || got[0].Name != "Fix" {
		t.Fatalf("project scope stripped: %+v", got)
	}
	if !got[0].PlatformMCP {
		t.Fatalf("platform MCP binding lost in catalog agent: %+v", got[0])
	}
	if got[0].Prompt != "" {
		t.Fatalf("catalog agent should stay slim: %+v", got[0])
	}
	if len(got[0].ArtifactOutputs) != 1 || len(got[0].Verification) != 1 {
		t.Fatalf("catalog agent should keep validation summary: %+v", got[0])
	}
	if !strings.HasSuffix(got[0].Description, "…") || len(got[0].Description) >= len(source[0].Description) {
		t.Fatalf("description not clipped: %q", got[0].Description)
	}
	if source[0].Prompt == "" {
		t.Fatal("catalogAgents mutated source")
	}
}

func TestAgentCloneStampsProjectAndKeepsBody(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, project := range []domain.Project{{ID: "other", Key: "OTHER", Name: "Other"}, {ID: "gslb", Key: "GSLB", Name: "GSLB"}} {
		if err := s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAgent(domain.AgentDefinition{
		ID: "src-role", Name: "Source", Version: 3, ProjectID: "other", Shared: true,
		Prompt: "do it",
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	copied := requestJSON[domain.AgentDefinition](t, h, http.MethodPost, "/api/agents/src-role/clone", map[string]any{
		"id": "dst-role", "name": "Copy", "projectId": "gslb",
	}, http.StatusCreated)
	if copied.ID != "dst-role" || copied.Name != "Copy" || copied.ProjectID != "gslb" || copied.Shared || copied.Version != 1 {
		t.Fatalf("clone = %+v", copied)
	}
	if copied.Prompt != "do it" {
		t.Fatalf("clone dropped role prompt: %q", copied.Prompt)
	}
	src, ok := s.Agent("src-role")
	if !ok || !src.Shared || src.ProjectID != "other" || src.Version != 3 {
		t.Fatalf("source mutated: %+v", src)
	}
}

func TestAgentDeleteRequiresUnusedAndUnshared(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []domain.AgentDefinition{
		{ID: "free-role", Name: "Free role", ProjectID: "alpha", Version: 1},
		{ID: "referenced-role", Name: "Referenced role", ProjectID: "alpha", Version: 1},
		{ID: "shared-role", Name: "Shared role", ProjectID: "alpha", Shared: true, Version: 1},
	} {
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "referenced-role"}},
	}, http.StatusOK)
	if saved.Nodes[0].AgentID != "referenced-role" {
		t.Fatalf("workflow was not saved: %+v", saved)
	}

	// referenced agent must be rejected with 409
	requestEmptyJSON[map[string]any](t, h, http.MethodDelete, "/api/agents/referenced-role", http.StatusConflict)
	if _, ok := s.Agent("referenced-role"); !ok {
		t.Fatal("referenced agent was deleted despite workflow usage")
	}
	// shared agent must be rejected with 409
	requestEmptyJSON[map[string]any](t, h, http.MethodDelete, "/api/agents/shared-role", http.StatusConflict)
	if _, ok := s.Agent("shared-role"); !ok {
		t.Fatal("shared agent was deleted despite shared flag")
	}
	// unused, unshared agent is deleted with 204
	doDelete := func(path string) int {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := doDelete("/api/agents/free-role"); code != http.StatusNoContent {
		t.Fatalf("free agent delete status = %d", code)
	}
	if _, ok := s.Agent("free-role"); ok {
		t.Fatal("free agent was not deleted")
	}
	// deleting again returns 404
	if code := doDelete("/api/agents/free-role"); code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d", code)
	}
}

func TestCatalogAssetDeleteRequiresUnusedAndUnshared(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutRule(domain.RuleDefinition{ID: "free-rule", Name: "Free rule", ProjectID: "alpha"}))
	must(s.PutRule(domain.RuleDefinition{ID: "used-rule", Name: "Used rule", ProjectID: "alpha"}))
	must(s.PutRule(domain.RuleDefinition{ID: "shared-rule", Name: "Shared rule", ProjectID: "alpha", Shared: true}))
	must(s.PutSkill(domain.SkillDefinition{ID: "free-skill", Name: "Free skill", ProjectID: "alpha"}))
	must(s.PutSkill(domain.SkillDefinition{ID: "used-skill", Name: "Used skill", ProjectID: "alpha"}))
	must(s.PutSkill(domain.SkillDefinition{ID: "shared-skill", Name: "Shared skill", ProjectID: "alpha", Shared: true}))
	must(s.PutSkillFiles("free-skill", []repo.SkillFileInput{{Path: "SKILL.md", Content: []byte("# free\n")}}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "free-df", Name: "Free df", ProjectID: "alpha", Content: "FROM scratch"}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "used-df", Name: "Used df", ProjectID: "alpha", Content: "FROM scratch"}))
	must(s.PutDockerfile(domain.DockerfileDefinition{ID: "shared-df", Name: "Shared df", ProjectID: "alpha", Shared: true, Content: "FROM scratch"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "free-env", Name: "Free env", ProjectID: "alpha"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "used-env", Name: "Used env", ProjectID: "alpha"}))
	must(s.PutEnvironment(domain.EnvironmentDefinition{ID: "shared-env", Name: "Shared env", ProjectID: "alpha", Shared: true}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "free-box", Name: "Free box", ProjectID: "alpha", Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "used-box", Name: "Used box", ProjectID: "alpha", Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "shared-box", Name: "Shared box", ProjectID: "alpha", Shared: true, Driver: "local"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "df-box", Name: "DF box", ProjectID: "alpha", Driver: "docker", DockerfileID: "used-df"}))
	must(s.PutSandbox(domain.SandboxProfile{ID: "env-box", Name: "Env box", ProjectID: "alpha", Driver: "seatbelt", EnvironmentID: "used-env"}))
	must(s.PutAgent(domain.AgentDefinition{
		ID: "asset-user", Name: "Asset user", ProjectID: "alpha", Version: 1,
		Rules: []string{"used-rule"}, Skills: []string{"used-skill"},
	}))
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "used-box",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "asset-user"}},
	}, http.StatusOK)

	doDelete := func(path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	assertConflict := func(path string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s status = %d, want 409; body %s", path, rec.Code, rec.Body.String())
		}
	}
	assertGone := func(path string, stillThere func() bool, label string) {
		t.Helper()
		if code := doDelete(path); code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want 204", path, code)
		}
		if stillThere() {
			t.Fatalf("%s was not deleted", label)
		}
		if code := doDelete(path); code != http.StatusNotFound {
			t.Fatalf("repeat %s status = %d, want 404", path, code)
		}
	}

	if code := doDelete("/api/rules"); code != http.StatusBadRequest {
		t.Fatalf("missing id status = %d", code)
	}
	assertConflict("/api/rules?id=used-rule")
	assertConflict("/api/rules?id=shared-rule")
	assertGone("/api/rules?id=free-rule", func() bool { _, ok := s.Rule("free-rule"); return ok }, "free rule")

	assertConflict("/api/skills?id=used-skill")
	assertConflict("/api/skills?id=shared-skill")
	assertGone("/api/skills?id=free-skill", func() bool { _, ok := s.Skill("free-skill"); return ok }, "free skill")
	if _, ok := s.SkillFileContent("free-skill", "SKILL.md"); ok {
		t.Fatal("skill_files rows were not cascaded")
	}

	assertConflict("/api/dockerfiles?id=used-df")
	assertConflict("/api/dockerfiles?id=shared-df")
	assertGone("/api/dockerfiles?id=free-df", func() bool { _, ok := s.Dockerfile("free-df"); return ok }, "free dockerfile")

	assertConflict("/api/environments?id=used-env")
	assertConflict("/api/environments?id=shared-env")
	assertGone("/api/environments?id=free-env", func() bool { _, ok := s.Environment("free-env"); return ok }, "free environment")

	assertConflict("/api/sandboxes?id=used-box")
	assertConflict("/api/sandboxes?id=shared-box")
	assertConflict("/api/sandboxes?id=local")
	assertConflict("/api/sandboxes?id=" + repo.GenericDockerSandboxID)
	assertConflict("/api/dockerfiles?id=" + repo.GenericBackendDockerfileID)
	assertGone("/api/sandboxes?id=free-box", func() bool { _, ok := s.Sandbox("free-box"); return ok }, "free sandbox")
}

func TestSharingAgentCascadesToOwnedRulesAndSkills(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRule(domain.RuleDefinition{ID: "owned-rule", Name: "Owned rule", ProjectID: "gslb"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSkill(domain.SkillDefinition{ID: "owned-skill", Name: "Owned skill", ProjectID: "gslb"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "shared-role", Name: "Shared", Version: 1, ProjectID: "gslb", Shared: true,
		Rules: []string{"owned-rule"}, Skills: []string{"owned-skill"}, ExecutorID: "codebuddy",
	}, http.StatusOK)
	rule, _ := s.Rule("owned-rule")
	skill, _ := s.Skill("owned-skill")
	if !rule.Shared || !skill.Shared {
		t.Fatalf("dependencies were not shared: rule=%+v skill=%+v", rule, skill)
	}
}

func TestPutAgentRejectsCommandGateWithoutExecutionScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "gslb", Key: "GSLB", Name: "GSLB"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	errBody := requestJSON[map[string]string](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "scoped-role", Name: "Scoped", Version: 1, ProjectID: "gslb", ExecutorID: "codebuddy",
		Verification: []domain.VerificationGate{{ID: "plan", Name: "plan", Mode: "command", Commands: []string{`test -f fix-plan.md`}}},
	}, http.StatusBadRequest)
	if !strings.Contains(errBody["error"], "executionScope") {
		t.Fatalf("error = %#v", errBody)
	}
	saved := requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", domain.AgentDefinition{
		ID: "scoped-role", Name: "Scoped", Version: 1, ProjectID: "gslb", ExecutorID: "codebuddy",
		Verification: []domain.VerificationGate{{ID: "plan", Name: "plan", Mode: "command", ExecutionScope: domain.ExecutionScopeRun, Commands: []string{`test -f fix-plan.md`}}},
	}, http.StatusOK)
	if len(saved.Verification) != 1 || saved.Verification[0].ExecutionScope != domain.ExecutionScopeRun {
		t.Fatalf("saved gates = %#v", saved.Verification)
	}
}

func TestCloneAgentMCPServersCopiesByTargetScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, server := range []domain.MCPServerDefinition{
		{ID: "project-source", Name: "Project", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/project"},
		{ID: "personal-source", Name: "Personal", Scope: "personal", OwnerUserID: 9, Transport: "http", URL: "https://example.com/personal"},
	} {
		if err := s.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{deps: &deps{repo: s}}
	server.catalog = &catalogHandlers{deps: server.deps}
	ids, err := server.catalog.cloneAgentMCPServers([]string{"project-source", "personal-source"}, "target", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("cloned ids = %#v", ids)
	}
	project, ok := s.MCPServer(ids[0])
	if !ok || project.Scope != "project" || project.ProjectID != "target" || project.OwnerUserID != 0 {
		t.Fatalf("project MCP copy = %+v", project)
	}
	personal, ok := s.MCPServer(ids[1])
	if !ok || personal.Scope != "personal" || personal.OwnerUserID != 2 || personal.ProjectID != "" {
		t.Fatalf("personal MCP copy = %+v", personal)
	}
}

func TestRunListFallsBackToIssueProject(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	if err := s.PutIssue(domain.Issue{ID: "iss-legacy", ProjectID: "gslb", Title: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID: "legacy-run", WorkflowID: "issue-fix-production", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{IssueID: "iss-legacy"},
		CreatedAt:        now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	listed := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=gslb", nil, http.StatusOK)
	if len(listed) != 1 || listed[0].ID != "legacy-run" {
		t.Fatalf("gslb run list = %+v", listed)
	}
	other := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=platform", nil, http.StatusOK)
	for _, item := range other {
		if item.ID == "legacy-run" {
			t.Fatal("legacy run leaked into another project list")
		}
	}
}

func TestBootstrapAgentsKeepProjectScope(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutAgent(domain.AgentDefinition{
		ID: "owned-role", Name: "Owned", ProjectID: "gslb", Shared: true, Prompt: "do not leak",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "report", Path: "report.md", Required: true}},
		Verification:    []domain.VerificationGate{{ID: "report", Name: "report", Mode: "command", Commands: []string{"test -s report.md"}, ExecutionScope: domain.ExecutionScopeRun}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	boot := requestEmptyJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", http.StatusOK)
	raw, _ := boot["agents"].([]any)
	var found map[string]any
	for _, item := range raw {
		agent, _ := item.(map[string]any)
		if agent["id"] == "owned-role" {
			found = agent
			break
		}
	}
	if found == nil {
		t.Fatal("bootstrap missing owned-role")
	}
	if found["projectId"] != "gslb" || found["shared"] != true {
		t.Fatalf("bootstrap dropped project scope: %+v", found)
	}
	if _, leaked := found["prompt"]; leaked {
		t.Fatalf("bootstrap leaked the role prompt: %+v", found)
	}
	if artifacts, ok := found["artifactOutputs"].([]any); !ok || len(artifacts) != 1 {
		t.Fatalf("bootstrap dropped role artifact contracts: %+v", found)
	}
	if gates, ok := found["verification"].([]any); !ok || len(gates) != 1 {
		t.Fatalf("bootstrap dropped role verification gates: %+v", found)
	}
}

func TestProjectDefaultRepositoryMustAlreadyExist(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "other", Key: "OTHER", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "other-repo", ProjectID: "other", Name: "other", GongfengProjectID: "other/repo"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	missing := requestJSON[map[string]string](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "onboard", Key: "ONB", Name: "Onboard", DefaultRepositoryID: "onboard-repo",
	}, http.StatusBadRequest)
	if missing["error"] != "default repository must belong to project" {
		t.Fatalf("missing default repo: %+v", missing)
	}

	denied := requestJSON[map[string]string](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "stolen", Key: "STL", Name: "Stolen", DefaultRepositoryID: "other-repo",
	}, http.StatusBadRequest)
	if denied["error"] != "default repository must belong to project" {
		t.Fatalf("wrong-project default repo: %+v", denied)
	}

	created := requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{
		ID: "onboard", Key: "ONB", Name: "Onboard",
	}, http.StatusOK)
	if created.DefaultRepositoryID != "" {
		t.Fatalf("new project must not carry a default repo, got %q", created.DefaultRepositoryID)
	}
	repo := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"id": "onboard-repo", "projectId": "onboard", "repoUrl": "https://git.example.com/onboard/app",
	}, http.StatusOK)
	bound := requestJSON[domain.Project](t, h, http.MethodPut, "/api/projects", domain.Project{
		ID: "onboard", Name: "Onboard", DefaultRepositoryID: repo.ID,
	}, http.StatusOK)
	if bound.DefaultRepositoryID != "onboard-repo" {
		t.Fatalf("default repo after create: %q", bound.DefaultRepositoryID)
	}
}

func TestNewProjectDefaultsToGenericBackendDeliverySOP(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	created := requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{ID: "fresh", Key: "FRESH", Name: "Fresh"}, http.StatusOK)
	if created.WorkflowID != repo.GenericBackendDeliveryWorkflowID {
		t.Fatalf("new project should default to the LoopForge SOP, got %q", created.WorkflowID)
	}
	assertProjectHasDefaultTrackerMCPs(t, s, "fresh")
	// 显式指定或修改 SOP 不被覆盖。
	updated := requestJSON[domain.Project](t, h, http.MethodPut, "/api/projects", domain.Project{ID: "fresh", Name: "Fresh 2", WorkflowID: "custom-sop"}, http.StatusOK)
	if updated.WorkflowID != "custom-sop" {
		t.Fatalf("explicit SOP must be kept, got %q", updated.WorkflowID)
	}
}

func TestRepositoryOneLinkSetup(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[domain.Project](t, h, http.MethodPost, "/api/projects", domain.Project{ID: "linkproj", Key: "LINK", Name: "Link"}, http.StatusOK)

	// 只粘贴一个工蜂链接：自动补项目路径、仓库名、URL，主分支默认 master。
	created := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "linkproj",
		"repoUrl":   "https://git.example.com/infra/gateway/-/tree/master",
	}, http.StatusOK)
	if created.GongfengProjectID != "infra/gateway" || created.Name != "gateway" {
		t.Fatalf("unexpected parse: %+v", created)
	}
	if created.DefaultBranch != "master" {
		t.Fatalf("default branch = %q, want master", created.DefaultBranch)
	}
	if created.IntegrateBranch != "" {
		t.Fatalf("new repo must not invent an integrate branch, got %q", created.IntegrateBranch)
	}
	if created.WebURL != "https://git.example.com/infra/gateway" || created.CloneURL != "https://git.example.com/infra/gateway.git" {
		t.Fatalf("unexpected urls: %+v", created)
	}

	// scp 形式同样可解析，显式字段优先于解析结果。
	updated := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId":     "linkproj",
		"repoUrl":       "git@git.example.com:infra/storage.git",
		"name":          "存储",
		"defaultBranch": "release",
	}, http.StatusOK)
	if updated.Provider != domain.RepositoryProviderSSH || updated.GongfengProjectID != "" || updated.Name != "存储" || updated.DefaultBranch != "release" {
		t.Fatalf("unexpected scp parse / field priority: %+v", updated)
	}
	if updated.CloneURL != "git@git.example.com:infra/storage.git" {
		t.Fatalf("ssh paste should keep ssh clone url, got %q", updated.CloneURL)
	}

	cloned := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "linkproj",
		"repoUrl":   "git clone git@git.example.com:infra/dns.git",
	}, http.StatusOK)
	if cloned.Provider != domain.RepositoryProviderSSH || cloned.GongfengProjectID != "" || cloned.CloneURL != "git@git.example.com:infra/dns.git" {
		t.Fatalf("git clone prefix was not parsed: %+v", cloned)
	}

	// 项目 ID 字段本身粘贴完整链接（内联行/弹窗都可能发生）：必须规范化为
	// group/repo 而不是把 URL 当作项目路径入库，否则同步、建分支和 MR 全挂。
	pasted := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
		"projectId":         "linkproj",
		"gongfengProjectId": "https://git.example.com/infra/cache",
	}, http.StatusOK)
	if pasted.GongfengProjectID != "infra/cache" {
		t.Fatalf("full link in project id must normalize, got %q", pasted.GongfengProjectID)
	}
	if pasted.WebURL != "https://git.example.com/infra/cache" || pasted.CloneURL != "https://git.example.com/infra/cache.git" {
		t.Fatalf("urls derived from normalized id are wrong: %+v", pasted)
	}

	// 链接形态但解析失败（非工蜂域名、缺项目路径等）必须 400，绝不能把 URL 当项目路径落库。
	for _, bad := range []string{
		"https://gitlab.invalid/infra/cache",
		"git@gitlab.invalid:infra/cache.git",
		"https://git.example.com",
		"https://",
		"infra/../etc",
		"infra/repo name",
		`infra\repo`,
	} {
		resp := requestJSON[map[string]string](t, h, http.MethodPost, "/api/repositories", map[string]any{
			"projectId":         "linkproj",
			"gongfengProjectId": bad,
		}, http.StatusBadRequest)
		if resp["error"] == "" {
			t.Fatalf("expected error message for %q", bad)
		}
	}

	// 普通项目路径与数字 ID 保持兼容。
	for _, good := range []string{"tencent/infra/gateway", "12345", "legacy-project_id"} {
		stored := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{
			"projectId":         "linkproj",
			"gongfengProjectId": good,
		}, http.StatusOK)
		if stored.GongfengProjectID != good {
			t.Fatalf("plain path %q must be stored as-is, got %q", good, stored.GongfengProjectID)
		}
	}
}

func TestGlobalRuleWorkflowBinding(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "beta", Key: "BETA", Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "alpha-sop", Name: "Alpha SOP", ProjectID: "alpha", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "dev", Name: "实现", Type: "agent", AgentID: "developer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "beta-private", Name: "Beta private", ProjectID: "beta", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "dev", Name: "实现", Type: "agent", AgentID: "developer"}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守", Priority: 50,
		Global: true, WorkflowIDs: []string{" alpha-sop ", "alpha-sop", ""},
	}, http.StatusOK)
	saved, ok := s.Rule("alpha-global")
	if !ok || !saved.Global || len(saved.WorkflowIDs) != 1 || saved.WorkflowIDs[0] != "alpha-sop" {
		t.Fatalf("saved rule = %#v", saved)
	}
	missing := requestJSON[map[string]string](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: true, WorkflowIDs: []string{"missing-sop"},
	}, http.StatusBadRequest)
	if missing["error"] == "" {
		t.Fatal("expected missing SOP error")
	}
	foreign := requestJSON[map[string]string](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: true, WorkflowIDs: []string{"beta-private"},
	}, http.StatusBadRequest)
	if foreign["error"] == "" {
		t.Fatal("expected foreign SOP error")
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/rules", domain.RuleDefinition{
		ID: "alpha-global", Name: "全局约束", ProjectID: "alpha", Content: "所有节点都要遵守",
		Global: false, WorkflowIDs: []string{"alpha-sop"},
	}, http.StatusOK)
	cleared, _ := s.Rule("alpha-global")
	if cleared.Global || len(cleared.WorkflowIDs) != 0 {
		t.Fatalf("turning the switch off must drop SOP bindings: %#v", cleared)
	}
}

func TestExecutorModelsCatalogUsesOnlyTheEditorsSavedModels(t *testing.T) {
	assistant, user := workbenchFixture(t)
	user = domain.User{
		LoginName:        "personal-catalog",
		CodexAPIKey:      "fixture-secret",
		CodexModels:      []domain.CLIModel{{ID: "gateway/second", Name: "Second"}, {ID: "gateway/first"}},
		ClaudeCodeModels: []domain.CLIModel{{ID: "custom-sonnet", Name: "Personal Sonnet"}},
		AgentCLICredentials: map[string]domain.AgentCLICredential{
			"opencode": {Models: []domain.CLIModel{{ID: "alibaba/qwen3.7-plus", Name: "Qwen3.7 Plus"}}},
		},
	}
	if err := assistant.repo.PutUser(user); err != nil {
		t.Fatal(err)
	}
	user, _ = assistant.repo.UserByLoginName(user.LoginName)
	handler := &catalogHandlers{deps: assistant.deps}
	c, rec := workbenchContext(user, "GET", "/api/executor-models", bytes.NewBuffer(nil))
	handler.executorModels(c)
	var catalogs map[string]executorModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &catalogs) != nil {
		t.Fatalf("catalog request: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(rec.Body.String(), "fixture-secret") {
		t.Fatal("personal model catalog or credentials were cacheable or leaked")
	}
	if models := catalogs["codex"].Models; len(models) != 2 || models[0].Name != "Second" || models[1].ID != "gateway/first" || models[1].Name != "gateway/first" {
		t.Fatalf("Codex models = %+v", models)
	}
	if models := catalogs["claude"].Models; len(models) != 1 || models[0].Name != "Personal Sonnet" {
		t.Fatalf("Claude models = %+v", models)
	}
	if models := catalogs["opencode"].Models; len(models) != 1 || models[0].Name != "Qwen3.7 Plus" || models[0].Family != "qwen" {
		t.Fatalf("OpenCode models = %+v", models)
	}
	if !catalogs["codex"].CustomModel || len(catalogs["codex"].Efforts) == 0 {
		t.Fatalf("built-in Codex options disappeared: %+v", catalogs["codex"])
	}
	for _, key := range []string{"codex", "claude"} {
		if catalogs[key].Source != "personal" || catalogs[key].Availability != executorModelsVerified || catalogs[key].Notice != "" {
			t.Fatalf("%s saved catalog not marked verified: %+v", key, catalogs[key])
		}
	}
	// workbench-owner only has a CodeBuddy key (workbenchFixture): no Codex
	// credential and no runner, so its Codex list is empty (state none)
	// instead of the built-in list, and never the other user's models.
	other, _ := assistant.repo.UserByLoginName("workbench-owner")
	c, rec = workbenchContext(other, "GET", "/api/executor-models", bytes.NewBuffer(nil))
	handler.executorModels(c)
	var others map[string]executorModelCatalog
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &others) != nil || strings.Contains(rec.Body.String(), "gateway/second") {
		t.Fatalf("other user catalog: %d %s", rec.Code, rec.Body)
	}
	if codex := others["codex"]; codex.Availability != executorModelsNone || len(codex.Models) != 0 || !codex.CustomModel {
		t.Fatalf("other user Codex catalog = %+v", codex)
	}
}

// The role catalog lists only the Codex / Claude Code models the editor can
// use, and says so when it cannot verify them.
func TestExecutorModelsAvailabilityStates(t *testing.T) {
	assistant, owner := workbenchFixture(t)
	handler := &catalogHandlers{deps: assistant.deps}
	const claudeKey, claudeBase = "availability-claude-key", "https://claude-gateway.example"
	discover := assistantEngineDiscover
	assistantEngineDiscover = func(_ context.Context, anthropic bool, key, baseURL string) ([]assistantDiscoveredModel, error) {
		if !anthropic || key != claudeKey || baseURL != claudeBase {
			t.Errorf("unexpected discovery anthropic=%t base=%s", anthropic, baseURL)
			return nil, nil
		}
		return []assistantDiscoveredModel{{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"}}, nil
	}
	t.Cleanup(func() {
		assistantEngineDiscover = discover
		assistantDiscoveryCache.Delete(assistantDiscoveryKey(runtime.AgentCLIProtocolMessages, claudeKey, claudeBase))
	})
	put := func(u domain.User) domain.User {
		t.Helper()
		if err := assistant.repo.PutUser(u); err != nil {
			t.Fatal(err)
		}
		stored, _ := assistant.repo.UserByLoginName(u.LoginName)
		return stored
	}
	get := func(u domain.User, query string) map[string]executorModelCatalog {
		t.Helper()
		c, rec := workbenchContext(u, http.MethodGet, "/api/executor-models"+query, bytes.NewBuffer(nil))
		handler.executorModels(c)
		var out map[string]executorModelCatalog
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), claudeKey) || strings.Contains(rec.Body.String(), claudeBase) {
			t.Fatal("catalog leaked a credential")
		}
		return out
	}
	ids := func(models []executorModelInfo) []string {
		out := make([]string, 0, len(models))
		for _, model := range models {
			out = append(out, model.ID)
		}
		return out
	}

	// AC-1 / AC-2: saved catalog and Base URL probe are verified lists.
	verified := put(domain.User{
		LoginName: "availability-verified", CodexModels: []domain.CLIModel{{ID: "gateway-a"}},
		ClaudeCodeAPIKey: claudeKey, ClaudeCodeBaseURL: claudeBase,
	})
	catalogs := get(verified, "")
	if codex := catalogs["codex"]; codex.Source != "personal" || codex.Availability != executorModelsVerified ||
		strings.Join(ids(codex.Models), ",") != "gateway-a" || codex.Notice != "" {
		t.Fatalf("verified Codex catalog = %+v", codex)
	}
	if claude := catalogs["claude"]; claude.Source != "personal" || claude.Availability != executorModelsVerified ||
		strings.Join(ids(claude.Models), ",") != "claude-opus-5-5" {
		t.Fatalf("probed Claude catalog = %+v", claude)
	}

	// AC-3: a key on the official endpoint cannot be verified.
	keyOnly := put(domain.User{LoginName: "availability-key-only", CodexAPIKey: "official-key"})
	catalogs = get(keyOnly, "")
	codex := catalogs["codex"]
	if codex.Source != "builtin" || codex.Availability != executorModelsUnverified || codex.Notice == "" ||
		codex.Default != "" || !slices.Contains(ids(codex.Models), "gpt-6.2-astra") || !codex.CustomModel {
		t.Fatalf("unverified Codex catalog = %+v", codex)
	}
	if claude := catalogs["claude"]; claude.Availability != executorModelsNone || len(claude.Models) != 0 {
		t.Fatalf("Claude without credential = %+v", claude)
	}

	// AC-5: a runner on local login is unverified; a runner on personal
	// credentials is not a login, and a runner without the CLI does not count.
	runnerUser := put(domain.User{LoginName: "availability-runner"})
	if err := assistant.repo.PutRunner(domain.Runner{ID: "availability-runner-1", Name: "laptop", OwnerUserID: runnerUser.ID, Runtimes: []domain.RunnerRuntime{
		{Provider: "codex", AuthMode: domain.RuntimeAuthLocal},
		{Provider: "claude-code", AuthMode: domain.RuntimeAuthPersonal},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := assistant.repo.PutRunner(domain.Runner{ID: "availability-runner-2", Name: "empty", OwnerUserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	catalogs = get(runnerUser, "")
	if catalogs["codex"].Availability != executorModelsUnverified || len(catalogs["codex"].Models) == 0 {
		t.Fatalf("runner login Codex catalog = %+v", catalogs["codex"])
	}
	if catalogs["claude"].Availability != executorModelsNone || len(catalogs["claude"].Models) != 0 {
		t.Fatalf("runner personal-mode Claude catalog = %+v", catalogs["claude"])
	}

	// AC-4 / AC-14: no credential and only a runner without these CLIs: none;
	// every other CLI is exactly the built-in catalog.
	catalogs = get(owner, "")
	builtin := get(owner, "?builtin=1")
	for _, key := range []string{"codex", "claude"} {
		if catalogs[key].Availability != executorModelsNone || len(catalogs[key].Models) != 0 || !strings.Contains(catalogs[key].Notice, "个人配置") {
			t.Fatalf("%s none catalog = %+v", key, catalogs[key])
		}
		if len(builtin[key].Models) == 0 || builtin[key].Availability != "" {
			t.Fatalf("?builtin=1 %s must keep the full list: %+v", key, builtin[key])
		}
	}
	for _, key := range []string{"kiro", "cursor", "agy", "kimi", "dsh", "zcode"} {
		got, _ := json.Marshal(catalogs[key])
		want, _ := json.Marshal(builtin[key])
		if string(got) != string(want) {
			t.Errorf("%s catalog changed:\n got %s\nwant %s", key, got, want)
		}
	}
}

// AC-12: the generic template role editor applies the same model check.
func TestGenericTemplateAgentModelValidation(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "generic-admin-session-secret")
	h, s := newGenericTemplateServer(t)
	if err := s.PutUser(domain.User{LoginName: "example-admin", CodexModels: []domain.CLIModel{{ID: "gateway-a"}}}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.UserByLoginName("example-admin")
	path := "/api/workflows/" + repo.GenericBackendDeliveryWorkflowID + "/agents"
	listed := requestEmptyJSON[[]domain.AgentDefinition](t, h, http.MethodGet, path, http.StatusOK)
	if len(listed) == 0 {
		t.Fatal("generic template has no roles")
	}
	role := listed[0]
	role.ExecutorID, role.Model = "codex", "gpt-6.2-astra"
	rejected := requestJSONAuthed[map[string]string](t, h, admin, http.MethodPut, path, role, http.StatusBadRequest)
	if !strings.Contains(rejected["error"], "gpt-6.2-astra") || !strings.Contains(rejected["error"], "不在当前可用模型内") {
		t.Fatalf("rejection = %#v", rejected)
	}
	template, _ := s.GenericTemplateByWorkflow(repo.GenericBackendDeliveryWorkflowID)
	for _, agent := range template.Agents {
		if agent.ID == role.ID && agent.Model == "gpt-6.2-astra" {
			t.Fatal("a rejected model was stored")
		}
	}
	body := map[string]any{}
	raw, _ := json.Marshal(role)
	_ = json.Unmarshal(raw, &body)
	body["customModel"] = true
	saved := requestJSONAuthed[agentSaveResponse](t, h, admin, http.MethodPut, path, body, http.StatusOK)
	if saved.Model != "gpt-6.2-astra" || len(saved.Warnings) == 0 {
		t.Fatalf("manual model save = %+v", saved)
	}
	// Unchanged executor + model: other edits pass without a warning.
	role.Version = saved.Version
	role.Model, role.Description = "gpt-6.2-astra", "edited"
	again := requestJSONAuthed[agentSaveResponse](t, h, admin, http.MethodPut, path, role, http.StatusOK)
	if len(again.Warnings) != 0 || again.Description != "edited" {
		t.Fatalf("unchanged model save = %+v", again)
	}
	role.Version = again.Version
	role.Model = "gateway-a"
	if ok := requestJSONAuthed[agentSaveResponse](t, h, admin, http.MethodPut, path, role, http.StatusOK); ok.Model != "gateway-a" || len(ok.Warnings) != 0 {
		t.Fatalf("verified model save = %+v", ok)
	}
}
