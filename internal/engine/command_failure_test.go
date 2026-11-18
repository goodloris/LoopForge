package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D13: a failed script gate hands its command, exit code and output tail to
// the reworked upstream node and keeps them in its own node output.
func TestFailedScriptGateSendsOutputTailUpstream(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// 200 noise lines push the head out of the tail; the summary is last, as
	// pytest prints it. "signal: killed" in the log must not read as a
	// sandbox interrupt.
	gate := `i=0; while [ $i -lt 200 ]; do echo "noise line $i"; i=$((i+1)); done; echo "early-marker"; echo 'signal: killed'; echo 'FAILED tests/test_time.py::test_user_time_zone_plus_eight' >&2; echo '2 failed, 36 passed'; exit 3`
	wf := domain.WorkflowDefinition{ID: "gate-tail", Name: "gate tail", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "implement", Name: "implement", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "gate", Name: "gate", Type: "script", ScriptLanguage: "shell", Script: gate, MaxAttempts: 1, MaxReworks: 1, OnFailure: "reject", RejectTo: "implement"},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "implement", Target: "gate", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "failed")
	current, _ := s.Run(r.ID)
	implement := current.Nodes["implement"]
	if implement.ReworkCount != 1 || implement.Attempt != 2 {
		t.Fatalf("implement was not reworked once: %+v", implement)
	}
	for name, text := range map[string]string{"implement feedback": implement.Feedback, "gate output": current.Nodes["gate"].Output, "gate error": current.Nodes["gate"].Error} {
		for _, want := range []string{"退出码：3", "FAILED tests/test_time.py::test_user_time_zone_plus_eight", "2 failed, 36 passed", "echo '2 failed, 36 passed'", "前面的输出已省略"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, text)
			}
		}
		if name != "gate output" && !strings.Contains(text, "exit status 3") {
			t.Errorf("%s lacks the error: %s", name, text)
		}
		if strings.Contains(text, "noise line 0\n") {
			t.Errorf("%s keeps the head of the output instead of the tail:\n%s", name, text)
		}
	}
}

// The command detail must not leak into Error(): the engine classifies
// failures by substring.
func TestCommandFailureKeepsErrorClassification(t *testing.T) {
	cause := errors.New("Run: exit status 1")
	err := withCommandOutput(cause, "pytest", "max turns\nsignal: killed\nwithout a terminal result")
	if err.Error() != cause.Error() || !errors.Is(err, cause) {
		t.Fatalf("Error() = %q", err.Error())
	}
	if runtime.IsSandboxInterrupt(err) || isMaxTurnsErr(err) {
		t.Fatal("command output steered failure classification")
	}
	if got := failureFeedback(fmt.Errorf("verification tests: %w", err)); !strings.Contains(got, "signal: killed") || !strings.HasPrefix(got, "verification tests: Run: exit status 1") {
		t.Fatalf("feedback = %q", got)
	}
	if got := outputTail(strings.Repeat("界", 3000), 80, 4000); !strings.HasSuffix(got, "界") || len(got) > 4000+len("…（前面的输出已省略）\n") {
		t.Fatalf("tail cut a rune or overflowed: %d bytes", len(got))
	}
}

// An Agent node's command gate fails on the exit code even after the Agent
// submitted pass, and the reworked node gets the gate's output.
func TestAgentCommandGateFailureReworksUpstreamWithOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	marker := filepath.Join(dir, "gate-ran")
	putGateTestAgent(t, s, "exit-code-gate-agent", []domain.VerificationGate{{ID: "verify", Name: "验证命令", Mode: "command", Required: true, OnFailure: "reject", ExecutionScope: domain.ExecutionScopeRun,
		Commands: []string{fmt.Sprintf("if [ -f %[1]q ]; then exit 0; fi; touch %[1]q; echo 'FAILED test_page_one_starts_at_zero'; exit 1", marker)}}})
	wf := domain.WorkflowDefinition{ID: "agent-exit-gate", Name: "agent exit gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "implement", Name: "implement", Type: "script", ScriptLanguage: "shell", Script: "true", MaxAttempts: 1},
		{ID: "test", Name: "test", Type: "agent", AgentID: "exit-code-gate-agent", ExecutorID: "mock", MaxAttempts: 2, MaxReworks: 3, RejectTo: "implement"},
	}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "implement", Target: "test", When: "success"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	current, _ := s.Run(r.ID)
	if implement := current.Nodes["implement"]; implement.ReworkCount != 1 || implement.Attempt != 2 || !strings.Contains(implement.Feedback, "FAILED test_page_one_starts_at_zero") || !strings.Contains(implement.Feedback, "退出码：1") {
		t.Fatalf("gate failure must rework implement with the command output: %+v", implement)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("gate never ran")
	}
}
