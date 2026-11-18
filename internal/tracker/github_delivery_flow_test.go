package tracker

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// D16/D29: an SOP that opened a PR leaves the requirement waiting for the
// merge; merging into the integrate branch is not a delivery; only the work
// reaching the default branch (integrate promoted) marks it done.
func TestGitHubDeliveryWaitsForDefaultBranch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "gh", ProjectID: "shop", Provider: "github", Name: "shop", GongfengProjectID: "github:example/shop", DefaultBranch: "main", IntegrateBranch: "integration", WebURL: "https://github.com/example/shop"}); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "shop-1", ProjectID: "shop", Title: "fix", Status: "in_progress"}
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	pull := `{"number":9,"state":"open","head":{"ref":"work","sha":"work-head"},"base":{"ref":"integration"}}`
	compare := `{"status":"diverged"}`
	set := func(p, c string) { mu.Lock(); pull, compare = p, c; mu.Unlock() }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/shop/pulls/9":
			_, _ = w.Write([]byte(pull))
		case r.Method == "GET" && r.URL.Path == "/repos/example/shop/compare/main...work-head":
			_, _ = w.Write([]byte(compare))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.githubAPIBase = srv.URL
	poll := func() {
		// The sync only refreshes rows older than the stale window.
		_ = st.UpdateMRDeliveryState("github:example/shop", 9, "opened", "integration", time.Time{}, time.Now().UTC().Add(-time.Hour))
		s.SyncMRDeliveryStatesOnce(st)
	}
	check := func(step, status, deliveryState, mrState string) {
		t.Helper()
		got, _ := st.Issue(issue.ID)
		if got.Status != status || got.DeliveryState != deliveryState || got.DeliveryMRState != mrState {
			t.Fatalf("%s: status=%q deliveryState=%q mrState=%q, want %q/%q/%q", step, got.Status, got.DeliveryState, got.DeliveryMRState, status, deliveryState, mrState)
		}
	}

	// 1. SOP succeeded with the PR still open.
	url := domain.GongfengMRURL("github:example/shop", 9)
	if err := st.MarkIssueDeliveredFromSOPMRs(issue.ID, []domain.DeliveryMR{{Project: "github:example/shop", IID: 9, URL: url, GongfengState: "opened", TargetBranch: "integration"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check("PR open", "in_review", domain.DeliveryStatePending, "opened")
	poll()
	check("PR open after sync", "in_review", domain.DeliveryStatePending, "opened")

	// 2. PR merged into the integrate branch; main does not contain it yet.
	set(`{"number":9,"state":"closed","merged":true,"merged_at":"2026-11-18T09:34:42Z","head":{"ref":"work","sha":"work-head"},"base":{"ref":"integration"}}`, `{"status":"diverged"}`)
	poll()
	check("merged into integration", "in_review", domain.DeliveryStatePending, "integrated")
	if got, _ := st.Issue(issue.ID); got.Completed() || got.DeliveryLocked() {
		t.Fatalf("integration merge counted as delivered: %+v", got)
	}

	// 3. integration promoted to main: the work head is now behind main.
	set(pull, `{"status":"behind"}`)
	s.SyncMRDeliveryStatesOnce(st)
	check("promoted to main", "done", "merged", "merged")
}
