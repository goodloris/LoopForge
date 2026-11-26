package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestPreviewEnvironmentsCRUD(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	env := domain.PreviewEnvironment{ID: "pe-1", ProjectID: "p", IssueID: "i", RepositoryID: "r", RunnerID: "rn",
		LastRunID: "run-1", Branch: "b", SHA: "abc", Status: domain.PreviewEnvDeploying, AdminPassword: "pw"}
	if err := s.PutPreviewEnvironment(env); err != nil {
		t.Fatal(err)
	}
	got, ok := s.PreviewEnvironment("pe-1")
	if !ok || got.AdminPassword != "pw" || got.CreatedAt.IsZero() {
		t.Fatalf("get = %+v %v", got, ok)
	}
	created := got.CreatedAt
	got.Status, got.URL, got.DeployedAt, got.CreatedAt = domain.PreviewEnvReady, "http://x", time.Now(), time.Time{}
	if err := s.PutPreviewEnvironment(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.PreviewEnvironment("pe-1"); !again.CreatedAt.Equal(created) || again.Status != domain.PreviewEnvReady || again.DeployedAt.IsZero() {
		t.Fatalf("update = %+v", again)
	}
	if err := s.PutPreviewEnvironment(domain.PreviewEnvironment{ID: "pe-2", IssueID: "i", LastRunID: "run-2", Status: domain.PreviewEnvDeploying}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.PreviewEnvironmentsByIssue("i")); n != 2 {
		t.Fatalf("by issue = %d", n)
	}
	if list := s.PreviewEnvironmentsByRun("run-1"); len(list) != 1 || list[0].ID != "pe-1" {
		t.Fatalf("by run = %+v", list)
	}
	// Deploying environments cannot be claimed; ready ones exactly once.
	if _, ok, err := s.ClaimPreviewEnvironmentTeardown("pe-2", time.Now()); ok || err != nil {
		t.Fatalf("claimed deploying env: %v %v", ok, err)
	}
	if claimed, ok, err := s.ClaimPreviewEnvironmentTeardown("pe-1", time.Now().Add(-time.Hour)); !ok || err != nil || claimed.Status != domain.PreviewEnvDestroying {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-1", time.Now().Add(-time.Hour)); ok {
		t.Fatal("fresh destroying env claimed twice")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-1", time.Now().Add(time.Hour)); !ok {
		t.Fatal("stale destroying env not reclaimed")
	}
	got, _ = s.PreviewEnvironment("pe-1")
	got.Status = domain.PreviewEnvDestroyed
	_ = s.PutPreviewEnvironment(got)
	if live := s.LivePreviewEnvironments(); len(live) != 1 || live[0].ID != "pe-2" {
		t.Fatalf("live = %+v", live)
	}
}

func TestRunnerTaskKeepsPreviewSpec(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec := &domain.RunnerPreviewSpec{EnvID: "pe-1", SHA: "abc", Env: map[string]string{"PREVIEW_ADMIN_PASSWORD": "pw"}}
	if err := s.PutRunnerTask(domain.RunnerTask{ID: "t", RunnerID: "r", Provider: "terminal", Action: domain.RunnerTaskActionPreviewDeploy, Preview: spec}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.RunnerTask("t")
	if !ok || got.Preview == nil || got.Preview.EnvID != "pe-1" || got.Preview.Env["PREVIEW_ADMIN_PASSWORD"] != "pw" {
		t.Fatalf("task preview = %+v", got.Preview)
	}
}
