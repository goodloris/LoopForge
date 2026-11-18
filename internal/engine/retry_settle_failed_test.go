package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"path/filepath"
	"testing"
	"time"
)

// D28: a run whose nodes all succeeded but failed while settling (the
// integration merge) must be retryable without naming a node: the retry
// reruns the final node, which settles the run again.
func TestRetryFailedRunWithAllNodesSucceededRerunsFinalNode(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "settle-failed", Name: "settle failed", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "build", Name: "build", Type: "command", Command: "true", MaxAttempts: 1},
		{ID: "summary", Name: "summary", Type: "command", Command: "true", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "build-summary", Source: "build", Target: "summary", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	now := time.Now().UTC()
	run := domain.Run{ID: "run-settle-failed", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{
			"build":   {NodeID: "build", Status: "succeeded", Attempt: 1, FinishedAt: now},
			"summary": {NodeID: "summary", Status: "succeeded", Attempt: 1, FinishedAt: now},
		},
		Events: []domain.RunEvent{{Type: "run.failed", Message: "合入 integration 失败：GitHub refused the merge with HTTP 409", Timestamp: now}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(run.ID, "", "", ""); err != nil {
		t.Fatalf("retry a run that failed after every node succeeded: %v", err)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.Run(run.ID)
	if got.Nodes["build"].Attempt != 1 || got.Nodes["summary"].Attempt < 2 {
		t.Fatalf("retry must rerun only the final node: build=%+v summary=%+v", got.Nodes["build"], got.Nodes["summary"])
	}
}
