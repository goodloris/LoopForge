package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// D11: members watching a turn must see the confirmation mode and CLI the
// turn runs with, not their own picker settings. The generation carries both
// while it runs, and the stored reply keeps the mode.
func TestGenerationRecordsTurnApprovalModeAndExecutor(t *testing.T) {
	h, user := workbenchFixture(t)
	release := make(chan struct{})
	h.generate = func(context.Context, string, domain.User) (string, error) {
		<-release
		return "好的", nil
	}
	sid := "turn-mode"
	body, _ := json.Marshal(assistantRequest{SessionID: sid, ProjectID: "workbench", ApprovalMode: AssistantApprovalAssisted, Messages: []assistantMessage{{Role: "user", Content: "推送"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	running, _ := h.repo.AssistantGeneration(user.ID, sid)
	if running.ApprovalMode != AssistantApprovalAssisted || running.Executor != assistantEngineCodeBuddy {
		t.Fatalf("running turn = mode %q executor %q", running.ApprovalMode, running.Executor)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(user.ID, sid)
		if g.Status == "completed" {
			break
		}
		if g.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("generation: %+v", g)
		}
		time.Sleep(10 * time.Millisecond)
	}
	saved, _ := h.repo.AssistantSession(user.ID, sid)
	if reply := saved.Messages[len(saved.Messages)-1]; reply.Role != "assistant" || reply.ApprovalMode != AssistantApprovalAssisted {
		t.Fatalf("stored reply = %+v", reply)
	}
}
