package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

type verifyGateBody struct {
	Gate         bool                   `json:"gate"`
	Repositories []verifyGateRepository `json:"repositories"`
}

// GET /api/workflows/:id/verify-gate tells the UI whether the SOP's test gate
// will find .loopforge/verify, and degrades to unknown instead of failing.
func TestWorkflowVerifyGateReportsTargetBranchFile(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "shop-owner"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("shop-owner")
	// Since upstream v0.4.1 the owner's GitHub token is a github.com HTTPS credential.
	if err := st.SaveGitHTTPCredential(owner.ID, "", domain.GitHTTPCredentialSecret{GitHTTPCredential: domain.GitHTTPCredential{Name: "GitHub", Platform: domain.GitPlatformGitHub, Host: "github.com", Username: "x-access-token"}, Token: "owner-token"}); err != nil {
		t.Fatal(err)
	}
	gate := domain.VerificationGate{ID: verifyGateID, Name: "验证命令", Mode: "command", Stage: "after_execution", Commands: []string{"true"}, Required: true, OnFailure: "reject", ExecutionScope: "run"}
	for _, agent := range []domain.AgentDefinition{
		{ID: "shop-test", ProjectID: "shop", Name: "测试验证", ExecutorID: "codebuddy", Verification: []domain.VerificationGate{gate}},
		{ID: "shop-dev", ProjectID: "shop", Name: "开发实现", ExecutorID: "codebuddy"},
	} {
		if err := st.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "gated", Name: "Gated", ProjectID: "shop", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "dev", Name: "开发", Type: "agent", AgentID: "shop-dev", MaxAttempts: 1}, {ID: "test", Name: "测试", Type: "agent", AgentID: "shop-test", MaxAttempts: 1}}},
		{ID: "plain", Name: "Plain", ProjectID: "shop", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "dev", Name: "开发", Type: "agent", AgentID: "shop-dev", MaxAttempts: 1}}},
	} {
		if err := st.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop", Key: "SHOP", OwnerID: owner.ID, WorkflowID: "gated"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []domain.Repository{
		{ID: "with", ProjectID: "shop", Provider: "github", Name: "with", GongfengProjectID: "github:example/with", DefaultBranch: "main"},
		{ID: "without", ProjectID: "shop", Provider: "github", Name: "without", GongfengProjectID: "github:example/without", DefaultBranch: "develop"},
		{ID: "hidden", ProjectID: "shop", Provider: "github", Name: "hidden", GongfengProjectID: "github:example/hidden", DefaultBranch: "main"},
		{ID: "ssh", ProjectID: "shop", Provider: domain.RepositoryProviderSSH, Name: "ssh", CloneURL: "git@git.example:team/ssh.git", DefaultBranch: "main"},
	} {
		if err := st.PutRepository(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutIssue(domain.Issue{ID: "iss-1", ProjectID: "shop", Title: "Fix", Status: "todo", RepositoryIDs: []string{"without"}}); err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Errorf("GitHub read without the owner token: %s", r.URL)
		}
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/example/with/contents/.loopforge/verify?ref=main":
			_, _ = w.Write([]byte(`{"type":"file"}`))
		case "/repos/example/without/contents/.loopforge/verify?ref=develop", "/repos/example/without?":
			if r.URL.Path == "/repos/example/without" {
				_, _ = w.Write([]byte(`{"full_name":"example/without"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default: // hidden: the token cannot see the repository at all
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fake.Close()
	svc := tracker.NewService()
	svc.ConfigureWithStore(domain.SyncConfig{}, st)
	svc.SetGitHubAPIBase(fake.URL)
	h := &catalogHandlers{deps: &deps{repo: st, sync: svc}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/workflows/:id/verify-gate", h.workflow)

	got := requestJSON[verifyGateBody](t, r, http.MethodGet, "/api/workflows/gated/verify-gate", nil, http.StatusOK)
	status := map[string]string{}
	for _, repo := range got.Repositories {
		status[repo.ID] = repo.Status + "@" + repo.Branch
	}
	want := map[string]string{"with": "configured@main", "without": "missing@develop", "hidden": "unknown@main", "ssh": "unknown@main"}
	if !got.Gate || len(status) != len(want) {
		t.Fatalf("gate=%v repositories=%v", got.Gate, status)
	}
	for id, v := range want {
		if status[id] != v {
			t.Errorf("%s: got %q, want %q", id, status[id], v)
		}
	}

	issue := requestJSON[verifyGateBody](t, r, http.MethodGet, "/api/workflows/gated/verify-gate?issueId=iss-1", nil, http.StatusOK)
	if len(issue.Repositories) != 1 || issue.Repositories[0].ID != "without" || issue.Repositories[0].Status != "missing" {
		t.Fatalf("issue scope: %+v", issue.Repositories)
	}

	plain := requestJSON[verifyGateBody](t, r, http.MethodGet, "/api/workflows/plain/verify-gate", nil, http.StatusOK)
	if plain.Gate || len(plain.Repositories) != 0 {
		t.Fatalf("SOP without the gate: %+v", plain)
	}
}
