package schedule

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

// A trigger job pulls before dispatching only when it names a source; an
// empty source keeps dispatching issues already imported without a pull.
func TestTriggerJobPullsOnlyANamedSource(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "gh", ProjectID: "shop", Provider: "github", Name: "shop", GongfengProjectID: "github:example/shop", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	var pulls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pulls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer fake.Close()
	sync := tracker.NewService()
	sync.ConfigureWithStore(domain.SyncConfig{}, st)
	sync.SetGitHubAPIBase(fake.URL)
	svc := New(st, sync, func(domain.ScheduleJob, domain.Issue, string) error { return nil })
	for _, tc := range []struct {
		source string
		pulls  int32
	}{{"", 0}, {"github", 1}} {
		pulls.Store(0)
		job := domain.ScheduleJob{ID: "job-" + tc.source, ProjectID: "shop", Source: tc.source, WorkflowID: "wf", Enabled: true}
		if err := st.PutScheduleJob(job); err != nil {
			t.Fatal(err)
		}
		got, err := svc.RunJob(job.ID)
		if err != nil || got.LastStatus != "success" {
			t.Fatalf("source %q: status=%s err=%v lastErr=%q", tc.source, got.LastStatus, err, got.LastError)
		}
		if n := pulls.Load(); n != tc.pulls {
			t.Fatalf("source %q: %d GitHub pulls, want %d", tc.source, n, tc.pulls)
		}
	}
}
