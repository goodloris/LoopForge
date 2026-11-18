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
	promotes := `[]`
	set := func(p, c string) { mu.Lock(); pull, compare = p, c; mu.Unlock() }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/shop/pulls/9":
			_, _ = w.Write([]byte(pull))
		case r.Method == "GET" && r.URL.Path == "/repos/example/shop/pulls":
			if r.URL.Query().Get("head") != "example:integration" || r.URL.Query().Get("base") != "main" {
				t.Errorf("promote lookup must list integration → main pulls: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(promotes))
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
	metrics := func(step string, merged, integrated int, at time.Time) {
		t.Helper()
		got := st.Analytics(domain.AnalyticsFilter{ProjectID: "shop"})
		if got.Summary.MergedMRs != merged || got.Summary.IntegratedMRs != integrated {
			t.Fatalf("%s: mergedMrs=%d integratedMrs=%d, want %d/%d", step, got.Summary.MergedMRs, got.Summary.IntegratedMRs, merged, integrated)
		}
		if merged > 0 && (len(got.MRMerges) != 1 || !got.MRMerges[0].MergedAt.Equal(at)) {
			t.Fatalf("%s: merge must be dated by the promote: %+v", step, got.MRMerges)
		}
	}
	metrics("merged into integration", 0, 1, time.Time{})

	// 3. integration promoted to main by PR #20: the work head is now behind
	// main, and the promote PR dates the landing.
	promoteAt := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	mu.Lock()
	promotes = `[{"number":20,"state":"closed","merged":true,"merged_at":"2026-11-18T10:00:00Z","head":{"ref":"integration","sha":"int-head"},"base":{"ref":"main"}}]`
	mu.Unlock()
	set(pull, `{"status":"behind"}`)
	s.SyncMRDeliveryStatesOnce(st)
	check("promoted to main", "done", "merged", "merged")
	metrics("promoted to main", 1, 0, promoteAt)
}

// A promote without a promote PR (direct push to main) has no reliable
// landing time: the work MR is left out of mergedMrs instead of guessed.
func TestGitHubPromoteWithoutPromotePullIsNotDated(t *testing.T) {
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
	if err := st.PutIssue(domain.Issue{ID: "shop-1", ProjectID: "shop", Title: "fix", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/example/shop/pulls":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/example/shop/pulls/9":
			_, _ = w.Write([]byte(`{"number":9,"state":"closed","merged":true,"merged_at":"2026-11-18T09:34:42Z","head":{"ref":"work","sha":"work-head"},"base":{"ref":"integration"}}`))
		case "/repos/example/shop/compare/main...work-head":
			_, _ = w.Write([]byte(`{"status":"behind"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.githubAPIBase = srv.URL
	url := domain.GongfengMRURL("github:example/shop", 9)
	if err := st.MarkIssueDeliveredFromSOPMRs("shop-1", []domain.DeliveryMR{{Project: "github:example/shop", IID: 9, URL: url, GongfengState: "opened", TargetBranch: "integration"}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateMRDeliveryState("github:example/shop", 9, "merged", "integration", time.Date(2026, 11, 18, 9, 34, 42, 0, time.UTC), time.Now().UTC())
	s.SyncMRDeliveryStatesOnce(st)
	if got, _ := st.Issue("shop-1"); !got.DeliveryLocked() {
		t.Fatalf("work on main must still complete the requirement: %+v", got)
	}
	got := st.Analytics(domain.AnalyticsFilter{ProjectID: "shop"})
	if got.Summary.MergedMRs != 0 || got.Summary.IntegratedMRs != 1 {
		t.Fatalf("undated promote: mergedMrs=%d integratedMrs=%d, want 0/1", got.Summary.MergedMRs, got.Summary.IntegratedMRs)
	}
}
