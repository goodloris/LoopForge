package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// fakePreviewRunner completes queued preview tasks with answer and records them.
type fakePreviewRunner struct {
	mu    sync.Mutex
	tasks []domain.RunnerTask
	stop  chan struct{}
}

func startFakePreviewRunner(t *testing.T, e *Engine, answer func(domain.RunnerTask) (status, result, errMsg string)) *fakePreviewRunner {
	t.Helper()
	f := &fakePreviewRunner{stop: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		seen := map[string]bool{}
		for {
			select {
			case <-f.stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			for _, task := range e.repo.RunnerTasks() {
				if seen[task.ID] || task.Status != domain.RunnerTaskQueued || task.Preview == nil {
					continue
				}
				seen[task.ID] = true
				f.mu.Lock()
				f.tasks = append(f.tasks, task)
				f.mu.Unlock()
				task.Status, task.Result, task.Error = answer(task)
				_ = e.repo.PutRunnerTask(task)
				e.NotifyRunnerTaskChanged(task.ID)
			}
		}
	}()
	t.Cleanup(func() { close(f.stop); <-done })
	return f
}

func (f *fakePreviewRunner) dispatched() []domain.RunnerTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RunnerTask(nil), f.tasks...)
}

func previewTestConfig() ComposePreviewConfig {
	return ComposePreviewConfig{RunnerID: "preview-host", BaseURL: "http://{id}.10-0-0-1.sslip.io:8088", Secret: []byte(strings.Repeat("s", 32))}
}

// previewTestRepo returns a bare repository with branch "with" carrying the
// default compose file and branch "without" not carrying it.
func previewTestRepo(t *testing.T) (bare, withSHA, withoutSHA string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	testGit(t, root, "init", "-q", "--bare", bare)
	testGit(t, root, "init", "-q", work)
	testGit(t, work, "config", "user.email", "test@example.com")
	testGit(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "-q", "-m", "base")
	testGit(t, work, "push", "-q", bare, "HEAD:refs/heads/without")
	withoutSHA = testGit(t, work, "rev-parse", "HEAD")
	compose := filepath.Join(work, filepath.FromSlash(domain.PreviewDefaultComposeFile))
	if err := os.MkdirAll(filepath.Dir(compose), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "-q", "-m", "preview")
	testGit(t, work, "push", "-q", bare, "HEAD:refs/heads/with")
	withSHA = testGit(t, work, "rev-parse", "HEAD")
	return bare, withSHA, withoutSHA
}

func previewTestRun(bare string, branches ...string) domain.Run {
	run := domain.Run{
		ID:               "run-preview",
		WorkflowSnapshot: domain.WorkflowDefinition{Preview: &domain.PreviewContract{Enabled: true, Kind: domain.PreviewKindCompose}},
		ExecutionContext: domain.TelemetryContext{ProjectID: "proj", IssueID: "issue-1", ResponsibleUserID: 1},
	}
	for i, branch := range branches {
		id := []string{"repo-a", "repo-b"}[i]
		run.Repositories = append(run.Repositories, domain.RepositoryContext{RepositoryID: id, Name: id, CloneURL: bare, BaseBranch: "main", WorkBranch: branch})
	}
	return run
}

func previewHeads(run domain.Run, shas ...string) []domain.RunnerWorkBranchHead {
	var heads []domain.RunnerWorkBranchHead
	for i, rc := range run.Repositories {
		heads = append(heads, domain.RunnerWorkBranchHead{RepositoryID: rc.RepositoryID, Name: rc.Name, WorkBranch: rc.WorkBranch, Head: shas[i]})
	}
	return heads
}

func TestComposePreviewConfigProblem(t *testing.T) {
	if p := (ComposePreviewConfig{}).Problem(); !strings.Contains(p, "AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID") || !strings.Contains(p, "AGENTFLOW_COMPOSE_PREVIEW_SECRET") {
		t.Fatalf("missing config problem = %q", p)
	}
	cfg := previewTestConfig()
	if p := cfg.Problem(); p != "" {
		t.Fatal(p)
	}
	if cfg.URL("pe-1") != "http://pe-1.10-0-0-1.sslip.io:8088" || cfg.Host("pe-1") != "pe-1.10-0-0-1.sslip.io" {
		t.Fatalf("url/host = %s %s", cfg.URL("pe-1"), cfg.Host("pe-1"))
	}
	for _, bad := range []ComposePreviewConfig{
		{RunnerID: "r", BaseURL: "http://fixed:8088", Secret: cfg.Secret},
		{RunnerID: "r", BaseURL: "ftp://{id}.x", Secret: cfg.Secret},
		{RunnerID: "r", BaseURL: cfg.BaseURL, Secret: []byte("short")},
	} {
		if bad.Problem() == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestComposePreviewRunnerGate(t *testing.T) {
	e, _ := cvEEngine(t)
	cfg := previewTestConfig()
	if _, err := e.composePreviewRunner(ComposePreviewConfig{}); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("disabled config err = %v", err)
	}
	if _, err := e.composePreviewRunner(cfg); err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("missing runner err = %v", err)
	}
	if err := e.repo.PutRunner(domain.Runner{ID: cfg.RunnerID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionFeishu}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.composePreviewRunner(cfg); err == nil || !strings.Contains(err.Error(), domain.RunnerVersionComposePreview) {
		t.Fatalf("old runner err = %v", err)
	}
	if err := e.repo.PutRunner(domain.Runner{ID: cfg.RunnerID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionComposePreview}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.composePreviewRunner(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestDeployComposePreviews(t *testing.T) {
	e, _ := cvEEngine(t)
	cfg := previewTestConfig()
	runner := domain.Runner{ID: cfg.RunnerID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionComposePreview}
	if err := e.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	bare, withSHA, withoutSHA := previewTestRepo(t)
	fail := false
	fake := startFakePreviewRunner(t, e, func(task domain.RunnerTask) (string, string, string) {
		if fail {
			return domain.RunnerTaskFailed, "", "compose up: boom"
		}
		raw, _ := json.Marshal(domain.RunnerPreviewResult{Status: domain.PreviewEnvReady, SHA: task.Preview.SHA})
		return domain.RunnerTaskSuccess, string(raw), ""
	})
	ctx := context.Background()

	// repo-a has the compose file, repo-b does not: deploy one, skip one.
	run := previewTestRun(bare, "with", "without")
	out, err := e.deployComposePreviews(ctx, run, domain.PreviewDeployNodeID, cfg, runner, previewHeads(run, withSHA, withoutSHA))
	if err != nil {
		t.Fatal(err)
	}
	envID := domain.PreviewEnvID("issue-1", "repo-a")
	env, ok := e.repo.PreviewEnvironment(envID)
	if !ok || env.Status != domain.PreviewEnvReady || env.SHA != withSHA || env.URL != cfg.URL(envID) || env.AdminPassword == "" || env.LastRunID != run.ID || env.Branch != "with" {
		t.Fatalf("env = %+v %v", env, ok)
	}
	if !strings.Contains(out, env.URL) || !strings.Contains(out, "repo-b：已跳过") {
		t.Fatalf("output = %s", out)
	}
	if _, ok := e.repo.PreviewEnvironment(domain.PreviewEnvID("issue-1", "repo-b")); ok {
		t.Fatal("repository without compose file got an environment")
	}
	tasks := fake.dispatched()
	if len(tasks) != 1 {
		t.Fatalf("dispatched %d tasks", len(tasks))
	}
	spec, inv := tasks[0].Preview, tasks[0].Invocation
	if tasks[0].Action != domain.RunnerTaskActionPreviewDeploy || tasks[0].RunnerID != runner.ID || spec.EnvID != envID || spec.SHA != withSHA || spec.Ref != "with" ||
		spec.Host != cfg.Host(envID) || spec.Env["PREVIEW_ADMIN_PASSWORD"] != env.AdminPassword || spec.Env["LF_PREVIEW_ID"] != envID ||
		inv == nil || inv.GitHTTPUserID != 1 || len(inv.Repositories) != 1 || inv.Repositories[0].CloneURL != bare {
		t.Fatalf("task = %+v spec=%+v inv=%+v", tasks[0], spec, inv)
	}

	// Same SHA rerun: same env, URL and password; the runner decides no-op.
	rerun := run
	rerun.ID = "run-preview-2"
	if _, err := e.deployComposePreviews(ctx, rerun, domain.PreviewDeployNodeID, cfg, runner, previewHeads(rerun, withSHA, withoutSHA)); err != nil {
		t.Fatal(err)
	}
	again, _ := e.repo.PreviewEnvironment(envID)
	if again.AdminPassword != env.AdminPassword || again.URL != env.URL || again.LastRunID != "run-preview-2" || !again.CreatedAt.Equal(env.CreatedAt) {
		t.Fatalf("rerun env = %+v", again)
	}
	if n := len(fake.dispatched()); n != 2 {
		t.Fatalf("rerun dispatched %d tasks", n)
	}

	// Runner failure fails the node and records last_error.
	fail = true
	if _, err := e.deployComposePreviews(ctx, run, domain.PreviewDeployNodeID, cfg, runner, previewHeads(run, withSHA, withoutSHA)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure err = %v", err)
	}
	failed, _ := e.repo.PreviewEnvironment(envID)
	if failed.Status != domain.PreviewEnvFailed || !strings.Contains(failed.LastError, "boom") || failed.SHA != withSHA {
		t.Fatalf("failed env = %+v", failed)
	}

	// Unchanged repositories have no head and are skipped without dispatch.
	before := len(fake.dispatched())
	out, err = e.deployComposePreviews(ctx, run, domain.PreviewDeployNodeID, cfg, runner, nil)
	if err != nil || !strings.Contains(out, "没有改动") || len(fake.dispatched()) != before {
		t.Fatalf("no heads: %v %s", err, out)
	}
}

func TestComposePreviewTeardownSweep(t *testing.T) {
	e, _ := cvEEngine(t)
	cfg := previewTestConfig()
	e.SetComposePreview(cfg)
	if err := e.repo.PutRunner(domain.Runner{ID: cfg.RunnerID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionComposePreview}); err != nil {
		t.Fatal(err)
	}
	fake := startFakePreviewRunner(t, e, func(domain.RunnerTask) (string, string, string) {
		raw, _ := json.Marshal(domain.RunnerPreviewResult{Status: domain.PreviewEnvDestroyed, Leftover: &domain.RunnerPreviewLeftover{}})
		return domain.RunnerTaskSuccess, string(raw), ""
	})
	put := func(issue domain.Issue) {
		t.Helper()
		issue.ProjectID, issue.Title = "proj", issue.ID
		if err := e.repo.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	put(domain.Issue{ID: "open", Status: "in_review"})
	put(domain.Issue{ID: "done", Status: "done"})
	put(domain.Issue{ID: "merged", Status: "in_review", DeliveryMRs: []domain.DeliveryMR{{URL: "https://github.com/o/r/pull/1", State: "merged"}}})
	envs := map[string]domain.PreviewEnvironment{
		"pe-open":      {IssueID: "open", Status: domain.PreviewEnvReady},
		"pe-done":      {IssueID: "done", Status: domain.PreviewEnvReady},
		"pe-merged":    {IssueID: "merged", Status: domain.PreviewEnvFailed},
		"pe-deploying": {IssueID: "done", Status: domain.PreviewEnvDeploying},
		"pe-norun":     {Status: domain.PreviewEnvReady},
		"pe-gone":      {IssueID: "deleted-issue", Status: domain.PreviewEnvReady},
	}
	for id, env := range envs {
		env.ID, env.RunnerID = id, cfg.RunnerID
		if err := e.repo.PutPreviewEnvironment(env); err != nil {
			t.Fatal(err)
		}
	}
	if started := e.SweepPreviewTeardowns(); started != 3 {
		t.Fatalf("sweep started %d teardowns", started)
	}
	want := map[string]string{"pe-open": domain.PreviewEnvReady, "pe-done": domain.PreviewEnvDestroyed, "pe-merged": domain.PreviewEnvDestroyed,
		"pe-deploying": domain.PreviewEnvDeploying, "pe-norun": domain.PreviewEnvReady, "pe-gone": domain.PreviewEnvDestroyed}
	deadline := time.Now().Add(10 * time.Second)
	for id, status := range want {
		for {
			env, _ := e.repo.PreviewEnvironment(id)
			if env.Status == status {
				if status == domain.PreviewEnvDestroyed && env.DestroyedAt.IsZero() {
					t.Fatalf("%s destroyed without timestamp", id)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s status = %s, want %s", id, env.Status, status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for _, task := range fake.dispatched() {
		if task.Action != domain.RunnerTaskActionPreviewTeardown || task.Preview.EnvID == "pe-open" {
			t.Fatalf("unexpected task %+v", task)
		}
	}
	// Destroyed environments are not swept again; ready ones of open issues stay.
	if started := e.SweepPreviewTeardowns(); started != 0 {
		t.Fatalf("second sweep started %d", started)
	}
}
