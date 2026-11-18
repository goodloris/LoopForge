package engine

import (
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// seededTemplate reads one built-in SOP from the MySQL seed, as installed.
func seededTemplate(t *testing.T, id string) repo.GenericTemplate {
	t.Helper()
	raw, err := os.ReadFile("../../migrations/seed/001_public_catalog.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?m)^INSERT IGNORE INTO generic_templates .*VALUES \('` + regexp.QuoteMeta(id) + `','[^']*','(.*)',UTC_TIMESTAMP\(\)\);$`)
	match := pattern.FindSubmatch(raw)
	if match == nil {
		t.Fatalf("%s missing from the seed", id)
	}
	var template repo.GenericTemplate
	if err := json.Unmarshal([]byte(strings.NewReplacer(`\\`, `\`, `''`, `'`).Replace(string(match[1]))), &template); err != nil {
		t.Fatal(err)
	}
	return template
}

// D4 + D15 on the shipped defect-fix SOP: a REJECT from 独立评审 and a
// non-zero .loopforge/verify after 测试验证 passed both rework 开发实现, each
// time with one more attempt, and the run only reaches 人工验收 once the
// verify command exits 0. 开发实现 is a script here so the test can control
// .loopforge/verify; the review and test roles, the graph and the gate are
// the seeded ones (run without repositories, so the gate runs the workspace
// file).
func TestSeededDefectFixSOPReworksImplementOnReviewAndVerifyExitCode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	template := seededTemplate(t, "issue-fix-production")
	for _, rule := range template.Rules {
		if err := s.PutRule(rule); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range template.Agents {
		agent.ExecutorID = "mock"
		if agent.ID == "issue-fix-production-review" {
			agent.Prompt += "\n[[mock:gate=rework,pass]]"
		}
		if err := s.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	counter := filepath.Join(dir, "implement-runs")
	wf := template.Workflow
	wf.ID, wf.Generic, wf.SandboxProfileID = "seeded-defect-fix", false, "local"
	for i := range wf.Nodes {
		wf.Nodes[i].ExecutorID = "mock"
		if wf.Nodes[i].ID != "implement" {
			wf.Nodes[i].ArtifactOutputs = nil // the mock executor writes no documents
		} else {
			// Runs 1 and 2 leave a failing verify command, run 3 fixes it.
			wf.Nodes[i].Type, wf.Nodes[i].AgentID, wf.Nodes[i].ScriptLanguage = "script", "", "shell"
			wf.Nodes[i].Script = fmt.Sprintf(`n=$(cat %[1]q 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > %[1]q
mkdir -p .loopforge
if [ "$n" -lt 3 ]; then printf 'echo "FAILED test_page_one_starts_at_zero (run %%s)"\nexit 1\n' "$n" > .loopforge/verify; else printf 'exit 0\n' > .loopforge/verify; fi
echo "implementation $n" > implementation.md`, counter)
		}
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(dir, "workspaces")))
	r, err := e.Start(wf.ID, "修复分页")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	current, _ := s.Run(r.ID)
	if accept := current.Nodes["accept"]; accept.Status != "waiting_approval" {
		t.Fatalf("run must wait at 人工验收, accept=%+v test=%+v", accept, current.Nodes["test"])
	}
	implement := current.Nodes["implement"]
	if implement.ReworkCount != 2 || implement.Attempt != 3 {
		t.Fatalf("review REJECT and the failed verify must each rework 开发实现 once: %+v", implement)
	}
	if !strings.Contains(implement.Feedback, "FAILED test_page_one_starts_at_zero (run 2)") || !strings.Contains(implement.Feedback, "exit=1") {
		t.Fatalf("开发实现 must get the verify output: %q", implement.Feedback)
	}
	var reasons []string
	for _, event := range current.Events {
		if event.Type == "gate.rejected" {
			reasons = append(reasons, event.Message)
		}
	}
	if len(reasons) != 2 || !strings.Contains(reasons[0], "agent requested upstream rework") || !strings.Contains(reasons[1], "验证命令（.loopforge/verify）") {
		t.Fatalf("rework events = %q", reasons)
	}
}
