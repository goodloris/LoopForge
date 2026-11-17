package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestResolveCodeBuddyAPIKeyFromUserOnly(t *testing.T) {
	t.Setenv(codeBuddyAPIKeyEnv, "ck_host")
	t.Setenv(codeBuddyInternetEnv, "ioa")
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user", CodeBuddyInternetEnvironment: "private-net"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 22, LoginName: "newcomer"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveCodeBuddyCredential(s, 21); got.APIKey != "ck_user" || got.InternetEnvironment != "private-net" || got.Sponsored {
		t.Fatalf("typed user credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "" || got.Sponsored {
		t.Fatalf("user without key and no sponsor must not use host env, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 999); got.APIKey != "" || got.InternetEnvironment != "" {
		t.Fatalf("missing user must not use host env, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(nil, 21); got.APIKey != "" {
		t.Fatalf("nil store = %+v", got)
	}
}

// A user without a key of their own runs SOPs on the sponsor's key, marked
// sponsored so the engine pins the sponsored model.
func TestResolveCodeBuddyCredentialFallsBackToSponsor(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, user := range []domain.User{
		{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user"},
		{ID: 22, LoginName: "newcomer", CodeBuddyInternetEnvironment: "ioa"},
		{ID: 23, LoginName: "sponsor", CodeBuddyAPIKey: "ck_sponsor", CodeBuddyInternetEnvironment: "internal"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "sponsor")
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "ck_sponsor" || got.InternetEnvironment != "internal" || !got.Sponsored {
		t.Fatalf("newcomer credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 21); got.APIKey != "ck_user" || got.Sponsored {
		t.Fatalf("own key must win over sponsor, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 23); got.APIKey != "ck_sponsor" || got.Sponsored {
		t.Fatalf("sponsor runs on its own key, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 999); got.APIKey != "" {
		t.Fatalf("unknown user must not borrow the sponsor key, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 22); len(got.SponsoredModels) != 1 || got.SponsoredModels[0] != repo.SponsoredCodeBuddyModel {
		t.Fatalf("legacy sponsor models = %+v", got)
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "nobody")
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "" || got.Sponsored {
		t.Fatalf("missing sponsor account = %+v", got)
	}
}

// The key configured under 平台管理 wins over the legacy sponsor account and
// carries the administrator's model list.
func TestResolveCodeBuddyCredentialUsesPlatformSharedKey(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, user := range []domain.User{
		{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user"},
		{ID: 22, LoginName: "newcomer"},
		{ID: 23, LoginName: "sponsor", CodeBuddyAPIKey: "ck_sponsor"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "sponsor")
	if err := s.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "ck_platform", InternetEnvironment: "internal", Models: []string{"glm-5.1", "kimi-k2.6"}}, 21); err != nil {
		t.Fatal(err)
	}
	got := resolveCodeBuddyCredential(s, 22)
	if got.APIKey != "ck_platform" || got.InternetEnvironment != "internal" || !got.Sponsored || strings.Join(got.SponsoredModels, ",") != "glm-5.1,kimi-k2.6" {
		t.Fatalf("platform shared credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 23); got.APIKey != "ck_sponsor" || got.Sponsored {
		t.Fatalf("a user with a key keeps it, got %+v", got)
	}
	if err := s.ClearPlatformCodeBuddyKey(); err != nil {
		t.Fatal(err)
	}
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "ck_sponsor" || !got.Sponsored {
		t.Fatalf("clearing the platform key falls back to the legacy sponsor, got %+v", got)
	}
}

// A shared-key CodeBuddy node keeps the role's model when the administrator
// allowed it.
func TestSponsoredExecutorKeepsAllowedRoleModel(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, runtime.NewManager(t.TempDir()))
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	got := e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codebuddy", Model: "kimi-k2.6"}, []string{"glm-5.1", "kimi-k2.6"}, sink)
	if got.Model != "kimi-k2.6" || len(observed) != 1 || strings.Contains(observed[0].Message, "不生效") {
		t.Fatalf("allowed role model = %+v, notice %+v", got, observed)
	}
	got = e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codebuddy"}, []string{"glm-5.1", "kimi-k2.6"}, sink)
	if got.Model != "glm-5.1" {
		t.Fatalf("role without a model uses the shared default, got %+v", got)
	}
}

// A sponsored CodeBuddy node runs an allowed model whatever its role
// configures, says so on the node, and notes it once on the run.
func TestSponsoredExecutorPinsModelAndExplains(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := domain.WorkflowDefinition{ID: "sponsored", Name: "Sponsored", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err = st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	defer func() { _ = e.Cancel(run.ID) }()
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	for range 2 {
		got := e.sponsoredExecutor(run.ID, domain.ExecutorDefinition{ID: "codebuddy", Kind: "codebuddy", Model: "claude-opus-5", Effort: "high"}, []string{repo.SponsoredCodeBuddyModel}, sink)
		if got.Model != repo.SponsoredCodeBuddyModel || got.Effort != "high" || got.Kind != "codebuddy" {
			t.Fatalf("executor = %+v", got)
		}
	}
	if len(observed) != 2 || observed[0].Model != repo.SponsoredCodeBuddyModel || !strings.Contains(observed[0].Message, "共享 Key") || !strings.Contains(observed[0].Message, "claude-opus-5") || !strings.Contains(observed[0].Message, "不生效") {
		t.Fatalf("node notice = %+v", observed)
	}
	current, _ := st.RunState(run.ID)
	notes := 0
	for _, event := range current.Events {
		if event.Type == sponsoredRunEvent {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("run notes = %d, events %+v", notes, current.Events)
	}
}

// A CodeBuddy node whose responsible user has no key fails before the run
// sandbox is opened, so no sandbox image is built first. Kiro CLI and the
// platform shared key pass the check. The passing cases stop at a missing
// repository after the sandbox is opened, so no agent CLI runs.
func TestAgentNodeChecksKeyBeforeOpeningSandbox(t *testing.T) {
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	cases := []struct {
		name, executor string
		sharedKey      bool
		wantErr        string
	}{
		{name: "missing key", executor: "codebuddy", wantErr: "需求责任人未配置 CodeBuddy API Key，请在个人配置中配置"},
		{name: "kiro login", executor: "kiro", wantErr: "repository checkout"},
		{name: "shared key", executor: "codebuddy", sharedKey: true, wantErr: "repository checkout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.PutUser(domain.User{ID: 9, LoginName: "owner"}); err != nil {
				t.Fatal(err)
			}
			if tc.sharedKey {
				if err := s.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "ck_platform", Models: []string{"glm-5.1"}}, 9); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.PutAgent(domain.AgentDefinition{ID: "dev", Name: "dev", ExecutorID: tc.executor}); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{ID: "key-check", Name: "Key check", SandboxProfileID: "local",
				Nodes: []domain.WorkflowNode{{ID: "dev", Name: "Dev", Type: "agent", AgentID: "dev", MaxAttempts: 1}}}
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(t.TempDir()))
			missing := domain.RepositoryContext{RepositoryID: "repo", Name: "repo", LocalPath: filepath.Join(t.TempDir(), "missing"), BaseBranch: "main"}
			run, err := e.StartWithExecutionContext(wf.ID, "", missing, domain.TelemetryContext{ResponsibleUserID: 9})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, run.ID, "failed")
			finished, _ := s.Run(run.ID)
			node := finished.Nodes["dev"]
			if !strings.Contains(node.Error, tc.wantErr) || (tc.executor == "codebuddy" && !tc.sharedKey && node.Error != tc.wantErr) {
				t.Fatalf("node error = %q, want %q", node.Error, tc.wantErr)
			}
			if opened := finished.SandboxID != ""; opened != (tc.wantErr == "repository checkout") {
				t.Fatalf("sandbox opened = %v (%q), error %q", opened, finished.SandboxID, node.Error)
			}
		})
	}
}

// A missing key fails the node outright: sending the work back upstream
// cannot add a key, so the upstream node must not run again.
func TestMissingKeyDoesNotRejectUpstream(t *testing.T) {
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutUser(domain.User{ID: 9, LoginName: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "dev", Name: "dev", ExecutorID: "codebuddy"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "key-reject", Name: "Key reject", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "prep", Name: "Prep", Type: "command", Command: "printf ready > ready.txt", MaxAttempts: 1},
			{ID: "dev", Name: "Dev", Type: "agent", AgentID: "dev", RejectTo: "prep", MaxAttempts: 1, MaxReworks: 3},
		},
		Edges: []domain.WorkflowEdge{{ID: "prep-dev", Source: "prep", Target: "dev", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 9})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	finished, _ := s.Run(run.ID)
	if got := finished.Nodes["dev"].Error; got != "需求责任人未配置 CodeBuddy API Key，请在个人配置中配置" {
		t.Fatalf("dev error = %q", got)
	}
	if got := finished.Nodes["prep"].Attempt; got != 1 {
		t.Fatalf("prep attempt = %d, want 1 (no rework)", got)
	}
}
