package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestQueuedSOPDoesNotStartAndCancelDoesNotOccupy(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "queue-command", Name: "Queue", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "work", Type: "command", Command: "true", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.PutRun(domain.Run{ID: fmt.Sprintf("held-%d", i), Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}}); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("queue-worker", "queue-host", time.Minute, true)
	r, err := e.StartWithRepositoriesContext(wf.ID, "", nil, domain.TelemetryContext{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "queued" || r.OwnerInstanceID != "" || e.ActiveRunCount() != 0 {
		t.Fatalf("run=%+v active=%d", r, e.ActiveRunCount())
	}
	if r.Nodes["work"].Status != "pending" || r.SandboxID != "" {
		t.Fatal("queued SOP started")
	}
	if len(r.Events) != 1 || r.Events[0].Type != "run.queued" {
		t.Fatalf("events=%+v", r.Events)
	}
	if err := e.DeleteRun(r.ID); err == nil {
		t.Fatal("queued deletion allowed")
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := s.RunState(r.ID)
	if cancelled.Status != "cancelled" || cancelled.AdmissionState != "unstarted" {
		t.Fatal(cancelled)
	}
	if promoted, err := s.PromoteQueuedRuns(); err != nil || len(promoted) != 0 {
		t.Fatalf("promoted=%v err=%v", promoted, err)
	}
}

func TestQueuedSOPAutomaticallyStartsAfterSuccess(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "queue-approval", Name: "Queue", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.PutRun(domain.Run{ID: fmt.Sprintf("occupied-%d", i), Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}}); err != nil {
			t.Fatal(err)
		}
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("queue-worker", "queue-host", time.Minute, true)
	r, err := e.StartWithRepositoriesContext(wf.ID, "", nil, domain.TelemetryContext{ProjectID: "p"})
	if err != nil || r.Status != "queued" {
		t.Fatalf("run=%+v err=%v", r, err)
	}
	updates, unsubscribe, err := e.Subscribe(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	<-updates
	held, _ := s.RunState("occupied-0")
	held.Status = "succeeded"
	if err := s.PutRun(held); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterruptedRuns()
	waitRun(t, s, r.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" })
	select {
	case update := <-updates:
		if update.Status == "queued" {
			t.Fatal("queue promotion not published")
		}
	case <-time.After(time.Second):
		t.Fatal("no promotion event")
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	e.Drain(context.Background())
}

// A run parked on a human gate gives its project slot back; approving it
// while the project is full queues it, and it continues once a slot frees.
func TestWaitingGateReleasesSlotAndApprovalRequeues(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "gate-release", Name: "Gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("gate-worker", "gate-host", time.Minute, true)
	r, err := e.StartWithRepositoriesContext(wf.ID, "", nil, domain.TelemetryContext{ProjectID: "p"})
	if err != nil || r.Status != "running" {
		t.Fatalf("run=%+v err=%v", r, err)
	}
	waitRun(t, s, r.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "waiting" })
	// Ten more runs start although one run is parked on its gate.
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("busy-%d", i)
		if err := s.PutRun(domain.Run{ID: id, Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.RunState(id); got.Status != "running" {
			t.Fatalf("waiting gate still holds a slot: %s is %s", id, got.Status)
		}
	}
	if err := e.Approve(r.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunState(r.ID); got.Status != "queued" || got.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("approval bypassed the full project: status=%s gate=%s", got.Status, got.Nodes["gate"].Status)
	}
	busy, _ := s.RunState("busy-0")
	busy.Status = "failed"
	if err := s.PutRun(busy); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterruptedRuns()
	waitRun(t, s, r.ID, 3*time.Second, func(got domain.Run) bool { return got.Status == "succeeded" })
	e.Drain(context.Background())
}
