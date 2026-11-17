package repo

import (
	"testing"

	"agentflow/internal/domain"
)

// The sidebar headers and message counts are read with JSON SQL that each
// dialect spells differently; read failures are only logged, so assert values.
func TestSidebarRunMetasAndMessageCounts(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{ID: "meta-run", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{Name: "Bug fix"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.SidebarRunMetas([]string{"meta-run"})["meta-run"]; got.WorkflowName != "Bug fix" || got.Status != "running" {
		t.Fatalf("sidebar meta = %+v", got)
	}
	session := domain.AssistantSession{ID: "meta-chat", Messages: []domain.AssistantMessage{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}}}
	if err := s.PutAssistantSession(1, session); err != nil {
		t.Fatal(err)
	}
	if got := s.AssistantSessionMessageCounts([]string{"meta-chat"}); got["meta-chat"] != 2 {
		t.Fatalf("message counts = %v", got)
	}
}
