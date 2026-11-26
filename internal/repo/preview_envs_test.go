package repo

import (
	"fmt"
	"strings"
	"sync"
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
	// Fresh deploying environments cannot be claimed; ready ones exactly once.
	never := time.Now().Add(-time.Hour)
	if _, ok, err := s.ClaimPreviewEnvironmentTeardown("pe-2", never, never); ok || err != nil {
		t.Fatalf("claimed deploying env: %v %v", ok, err)
	}
	if claimed, ok, err := s.ClaimPreviewEnvironmentTeardown("pe-1", never, never); !ok || err != nil || claimed.Status != domain.PreviewEnvDestroying {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-1", never, never); ok {
		t.Fatal("fresh destroying env claimed twice")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-1", time.Now().Add(time.Hour), never); !ok {
		t.Fatal("stale destroying env not reclaimed")
	}
	if ok, err := s.FinishPreviewEnvironmentTeardown("pe-1", true, ""); !ok || err != nil {
		t.Fatalf("finish teardown = %v %v", ok, err)
	}
	if live := s.LivePreviewEnvironments(); len(live) != 1 || live[0].ID != "pe-2" {
		t.Fatalf("live = %+v", live)
	}
}

// age moves an environment's claim into the past, as after a restart.
func agePreviewEnvironment(t *testing.T, s *Store, id string, by time.Duration) {
	t.Helper()
	if err := s.DB.Model(&previewEnvironmentRow{}).Where("id = ?", id).Update("updated_at", time.Now().UTC().Add(-by)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPreviewEnvironmentDeployClaims(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stale := time.Now().Add(-40 * time.Minute)
	env := func(run, password string) domain.PreviewEnvironment {
		return domain.PreviewEnvironment{ID: "pe-c", ProjectID: "p", IssueID: "i", RepositoryID: "r", RunnerID: "rn", LastRunID: run, URL: "http://u", AdminPassword: password}
	}

	// Concurrent first deploys: exactly one inserts, one password survives.
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i], _ = s.ClaimPreviewEnvironmentDeploy(env(fmt.Sprintf("run-%d", i), fmt.Sprintf("pw-%d", i)), stale)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, ok := range results {
		if ok {
			winners++
		}
	}
	first, _ := s.PreviewEnvironment("pe-c")
	if winners != 1 || first.Status != domain.PreviewEnvDeploying || first.AdminPassword != "pw-"+strings.TrimPrefix(first.LastRunID, "run-") {
		t.Fatalf("winners=%d env=%+v", winners, first)
	}

	// A fresh deploying claim blocks another deploy and its outcome fence.
	if _, ok, _ := s.ClaimPreviewEnvironmentDeploy(env("run-x", "other"), stale); ok {
		t.Fatal("second deploy claimed a fresh deploying env")
	}
	if ok, _ := s.FinishPreviewEnvironmentDeploy("pe-c", "run-x", domain.PreviewEnvReady, "sha", ""); ok {
		t.Fatal("a run that does not hold the claim finished it")
	}

	// Restart: the deployer died; after the stale window a new deploy takes
	// over and keeps the stored password.
	agePreviewEnvironment(t, s, "pe-c", time.Hour)
	again, ok, err := s.ClaimPreviewEnvironmentDeploy(env("run-y", "new-pw"), stale)
	if err != nil || !ok || again.LastRunID != "run-y" || again.AdminPassword != first.AdminPassword {
		t.Fatalf("stale reclaim = %+v %v %v", again, ok, err)
	}
	if ok, _ := s.FinishPreviewEnvironmentDeploy("pe-c", first.LastRunID, domain.PreviewEnvFailed, "", "late"); ok {
		t.Fatal("the dead deployer's late outcome was written")
	}
	if ok, err := s.FinishPreviewEnvironmentDeploy("pe-c", "run-y", domain.PreviewEnvReady, "abc", ""); !ok || err != nil {
		t.Fatalf("finish = %v %v", ok, err)
	}
	if got, _ := s.PreviewEnvironment("pe-c"); got.Status != domain.PreviewEnvReady || got.SHA != "abc" || got.DeployedAt.IsZero() {
		t.Fatalf("ready env = %+v", got)
	}

	// A teardown claim blocks deploys; its outcome is not written over a
	// row that left destroying, and a stale deploy can be torn down.
	never := time.Now().Add(-time.Hour)
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", never, never); !ok {
		t.Fatal("teardown claim")
	}
	if cur, ok, _ := s.ClaimPreviewEnvironmentDeploy(env("run-z", "x"), stale); ok || cur.Status != domain.PreviewEnvDestroying {
		t.Fatalf("deploy over destroying = %+v %v", cur, ok)
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-c", true, ""); !ok {
		t.Fatal("finish teardown")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentDeploy(env("run-z", "x"), stale); !ok {
		t.Fatal("redeploy of a destroyed env")
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-c", true, ""); ok {
		t.Fatal("teardown outcome overwrote a redeploy")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", never, stale); ok {
		t.Fatal("fresh deploy torn down")
	}
	agePreviewEnvironment(t, s, "pe-c", time.Hour)
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", never, stale); !ok {
		t.Fatal("stale deploy not reclaimable by teardown")
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
