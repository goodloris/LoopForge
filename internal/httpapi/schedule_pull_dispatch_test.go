package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/schedule"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

// One trigger job set to "只同步 GitHub" pulls the open GitHub issues and
// dispatches them to its own SOP in the same "立即运行": the imported issues get
// project numbers after the local ones, and the project default SOP filled in
// on import does not override the job's SOP.
func TestScheduleJobRunPullsGitHubThenDispatches(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "shop.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "shop-owner", GitToken: "owner-token"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("shop-owner")
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "backend-sop", Name: "Backend", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}},
		{ID: "fix-sop", Name: "Fix", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}},
	} {
		if err := st.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutProject(domain.Project{ID: "shop", Name: "Shop", Key: "SHOP", OwnerID: owner.ID, WorkflowID: "backend-sop", DefaultRepositoryID: "gh-repo"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "gh-repo", ProjectID: "shop", Provider: "github", Name: "shop", GongfengProjectID: "github:example/shop", DefaultBranch: "main", WebURL: "https://github.com/example/shop", CloneURL: remote}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "local-1", ProjectID: "shop", Title: "Local", Status: "done", RepositoryID: "gh-repo"}); err != nil {
		t.Fatal(err)
	}
	pulls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/example/shop/issues" || r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pulls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":101,"number":1,"title":"Order total wrong","body":"b","state":"open","html_url":"https://github.com/example/shop/issues/1"},{"id":102,"number":2,"title":"Crash on empty cart","body":"b","state":"open","html_url":"https://github.com/example/shop/issues/2"}]`))
	}))
	defer fake.Close()

	svc := tracker.NewService()
	svc.ConfigureWithStore(domain.SyncConfig{}, st)
	svc.SetGitHubAPIBase(fake.URL)
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("test", "test", time.Minute, true)
	s := &Server{deps: &deps{repo: st, engine: e, sync: svc}}
	s.schedule = &scheduleHandlers{deps: s.deps}
	s.deps.schedule = schedule.New(st, svc, s.schedule.dispatchScheduled)
	s.deps.schedule.SetBaselineGuard(s.schedule.guardScheduledBaseline)
	s.deps.schedule.SetRunDispatcher(s.schedule.dispatchScheduledRun)
	t.Cleanup(func() {
		for _, run := range st.Runs() {
			_ = e.Cancel(run.ID)
		}
	})
	job := domain.ScheduleJob{ID: "job-gh", Name: "GitHub fixes", ProjectID: "shop", Source: "github", WorkflowID: "fix-sop", UseIssueWorkflow: true, Enabled: true, Cron: "0 9 * * *", IssueStatuses: []string{"backlog", "todo"}}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/schedule-jobs", s.schedule.scheduleJobs)
	r.POST("/api/schedule-jobs/:id/run", s.schedule.scheduleJobRun)
	requestJSON[domain.ScheduleJob](t, r, http.MethodPost, "/api/schedule-jobs/job-gh/run", map[string]any{}, http.StatusAccepted)
	var done domain.ScheduleJob
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if got, ok := st.ScheduleJob(job.ID); ok && got.LastStatus != "" && got.LastStatus != "running" {
			done = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("trigger job did not finish")
		}
	}
	if pulls != 1 || done.LastStatus != "success" || done.LastPulled != 2 || done.LastDispatched != 2 {
		t.Fatalf("pulls=%d job=%s err=%q pulled=%d dispatched=%d", pulls, done.LastStatus, done.LastError, done.LastPulled, done.LastDispatched)
	}
	numbers := map[int]string{}
	for _, issue := range st.Issues() {
		if prev, dup := numbers[issue.Number]; dup {
			t.Fatalf("issues %s and %s share number %d", prev, issue.ID, issue.Number)
		}
		numbers[issue.Number] = issue.ID
		if issue.Source != "github" {
			continue
		}
		if issue.Number <= 1 || issue.ExternalRef == nil || issue.ExternalRef.ExternalIID == 0 {
			t.Fatalf("imported issue numbering: %+v", issue)
		}
		if issue.LatestTaskID == "" {
			t.Fatalf("issue %s was not dispatched", issue.ID)
		}
		task, _ := st.Task(issue.LatestTaskID)
		if task.WorkflowID != "fix-sop" {
			t.Fatalf("issue %s dispatched to %q; the default SOP filled on import overrode the job SOP", issue.ID, task.WorkflowID)
		}
	}
}
