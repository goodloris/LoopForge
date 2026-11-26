package repo

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func previewClaimEnv(run, password string) domain.PreviewEnvironment {
	return domain.PreviewEnvironment{ID: "pe-c", ProjectID: "p", IssueID: "i", RepositoryID: "r", RunnerID: "rn", LastRunID: run, URL: "http://u", AdminPassword: password}
}

// agePreviewEnvironment moves an environment's claim into the past, as after a restart.
func agePreviewEnvironment(t *testing.T, s *Store, id string, by time.Duration) {
	t.Helper()
	if err := s.DB.Model(&previewEnvironmentRow{}).Where("id = ?", id).Update("updated_at", time.Now().UTC().Add(-by)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPreviewEnvironmentsQueries(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, env := range []domain.PreviewEnvironment{
		{ID: "pe-1", IssueID: "i", LastRunID: "run-1", Status: domain.PreviewEnvReady, AdminPassword: "pw"},
		{ID: "pe-2", IssueID: "i", LastRunID: "run-2", Status: domain.PreviewEnvDeploying},
		{ID: "pe-3", IssueID: "j", LastRunID: "run-1", Status: domain.PreviewEnvDestroyed},
	} {
		if err := SeedPreviewEnvironment(s, env, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := s.PreviewEnvironment("pe-1"); !ok || got.AdminPassword != "pw" || got.CreatedAt.IsZero() {
		t.Fatalf("get = %+v %v", got, ok)
	}
	if n := len(s.PreviewEnvironmentsByIssue("i")); n != 2 {
		t.Fatalf("by issue = %d", n)
	}
	if n := len(s.PreviewEnvironmentsByRun("run-1")); n != 2 {
		t.Fatalf("by run = %d", n)
	}
	if live := s.LivePreviewEnvironments(); len(live) != 2 {
		t.Fatalf("live = %+v", live)
	}
}

// R1: the first deploy is a plain insert and a duplicate key falls through
// to the conditional claim, whatever the driver reports as affected rows.
func TestPreviewEnvironmentDeployClaims(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stale := time.Now().Add(-40 * time.Minute)

	// Concurrent first deploys of different runs: exactly one inserts, one password survives.
	var wg sync.WaitGroup
	results := make([]bool, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i], errs[i] = s.ClaimPreviewEnvironmentDeploy(previewClaimEnv(fmt.Sprintf("run-%d", i), fmt.Sprintf("pw-%d", i)), fmt.Sprintf("n-%d", i), stale)
		}(i)
	}
	wg.Wait()
	winners := 0
	for i, ok := range results {
		if errs[i] != nil {
			t.Fatalf("claim %d: %v", i, errs[i])
		}
		if ok {
			winners++
		}
	}
	first, _ := s.PreviewEnvironment("pe-c")
	suffix := strings.TrimPrefix(first.LastRunID, "run-")
	if winners != 1 || first.Status != domain.PreviewEnvDeploying || first.AdminPassword != "pw-"+suffix {
		t.Fatalf("winners=%d env=%+v", winners, first)
	}
	firstNonce := "n-" + suffix

	// Another run is blocked by the fresh claim and cannot finish it.
	if _, ok, _ := s.ClaimPreviewEnvironmentDeploy(previewClaimEnv("run-x", "other"), "n-x", stale); ok {
		t.Fatal("another run claimed a fresh deploying env")
	}
	if ok, _ := s.FinishPreviewEnvironmentDeploy("pe-c", "n-x", domain.PreviewEnvReady, "sha", ""); ok {
		t.Fatal("an attempt without the claim finished it")
	}

	// R3: a new attempt of the same run (worker restarted) takes over its own
	// fresh claim at once; the old attempt's late outcome is rejected.
	retry, ok, err := s.ClaimPreviewEnvironmentDeploy(previewClaimEnv(first.LastRunID, "new-pw"), "n-retry", stale)
	if err != nil || !ok || retry.AdminPassword != first.AdminPassword {
		t.Fatalf("same-run takeover = %+v %v %v", retry, ok, err)
	}
	if ok, _ := s.FinishPreviewEnvironmentDeploy("pe-c", firstNonce, domain.PreviewEnvFailed, "", "late"); ok {
		t.Fatal("the superseded attempt's late outcome was written")
	}

	// Another run takes over only after the stale window, keeping the password.
	agePreviewEnvironment(t, s, "pe-c", time.Hour)
	again, ok, err := s.ClaimPreviewEnvironmentDeploy(previewClaimEnv("run-y", "new-pw"), "n-y", stale)
	if err != nil || !ok || again.LastRunID != "run-y" || again.AdminPassword != first.AdminPassword {
		t.Fatalf("stale reclaim = %+v %v %v", again, ok, err)
	}
	if ok, _ := s.FinishPreviewEnvironmentDeploy("pe-c", "n-retry", domain.PreviewEnvFailed, "", "late"); ok {
		t.Fatal("the dead deployer's late outcome was written")
	}
	if ok, err := s.FinishPreviewEnvironmentDeploy("pe-c", "n-y", domain.PreviewEnvReady, "abc", ""); !ok || err != nil {
		t.Fatalf("finish = %v %v", ok, err)
	}
	if got, _ := s.PreviewEnvironment("pe-c"); got.Status != domain.PreviewEnvReady || got.SHA != "abc" || got.DeployedAt.IsZero() {
		t.Fatalf("ready env = %+v", got)
	}

	// A teardown claim blocks deploys; its outcome is not written over a row
	// that left destroying, and a stale deploy can be torn down.
	never := time.Now().Add(-time.Hour)
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", "t-1", never, never); !ok {
		t.Fatal("teardown claim")
	}
	if cur, ok, _ := s.ClaimPreviewEnvironmentDeploy(previewClaimEnv("run-z", "x"), "n-z", stale); ok || cur.Status != domain.PreviewEnvDestroying {
		t.Fatalf("deploy over destroying = %+v %v", cur, ok)
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-c", "t-other", true, ""); ok {
		t.Fatal("a teardown without the claim finished it")
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-c", "t-1", true, ""); !ok {
		t.Fatal("finish teardown")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentDeploy(previewClaimEnv("run-z", "x"), "n-z", stale); !ok {
		t.Fatal("redeploy of a destroyed env")
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-c", "t-1", true, ""); ok {
		t.Fatal("teardown outcome overwrote a redeploy")
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", "t-2", never, stale); ok {
		t.Fatal("fresh deploy torn down")
	}
	agePreviewEnvironment(t, s, "pe-c", time.Hour)
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-c", "t-2", never, stale); !ok {
		t.Fatal("stale deploy not reclaimable by teardown")
	}
}

func TestPreviewEnvironmentTeardownClaims(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := SeedPreviewEnvironment(s, domain.PreviewEnvironment{ID: "pe-t", Status: domain.PreviewEnvReady}, ""); err != nil {
		t.Fatal(err)
	}
	never := time.Now().Add(-time.Hour)
	if claimed, ok, err := s.ClaimPreviewEnvironmentTeardown("pe-t", "t-1", never, never); !ok || err != nil || claimed.Status != domain.PreviewEnvDestroying {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-t", "t-2", never, never); ok {
		t.Fatal("teardown in progress claimed twice")
	}
	// R2: a failed teardown can be retried at once.
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-t", "t-1", false, "销毁失败：x"); !ok {
		t.Fatal("record failure")
	}
	retry, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-t", "t-2", never, never)
	if !ok || retry.LastError != "" {
		t.Fatalf("failed teardown not retried at once: %+v %v", retry, ok)
	}
	// A lost teardown (no outcome) is retried once stale.
	if _, ok, _ := s.ClaimPreviewEnvironmentTeardown("pe-t", "t-3", time.Now().Add(time.Hour), never); !ok {
		t.Fatal("stale teardown not reclaimed")
	}
	if ok, _ := s.FinishPreviewEnvironmentTeardown("pe-t", "t-2", true, ""); ok {
		t.Fatal("superseded teardown finished")
	}
	if ok, err := s.FinishPreviewEnvironmentTeardown("pe-t", "t-3", true, ""); !ok || err != nil {
		t.Fatalf("finish = %v %v", ok, err)
	}
	if got, _ := s.PreviewEnvironment("pe-t"); got.Status != domain.PreviewEnvDestroyed || got.DestroyedAt.IsZero() {
		t.Fatalf("destroyed = %+v", got)
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
