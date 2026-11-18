package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// D3: a shared conversation left in workspace mode by an earlier turn must
// still let the next speaker save a requirement and dispatch an SOP, as
// drafts kept in this conversation, without a direct platform write from
// inside the sandbox in any approval mode.
func TestWorkspaceModeTurnSubmitsPlatformDraftsInSameConversation(t *testing.T) {
	h, owner, peer, _, _ := sharedWorkbenchFixture(t)
	project, _ := h.repo.Project("workbench")
	run := domain.Run{ID: "ws-draft-run", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: project.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	chat := domain.AssistantSession{ID: "ws-draft-chat", ProjectID: project.ID, WorkspaceRunID: run.ID, OwnWorkspaceRunIDs: []string{run.ID}, RuntimeMode: domain.AssistantModeWorkspace,
		Messages: []domain.AssistantMessage{{Role: "user", AuthorID: owner.ID, Content: "改文案并提 PR"}, {Role: "assistant", Content: `{"message":"PR 已开","drafts":[]}`}}}
	if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetAssistantRuntimeMode(owner.ID, chat.ID, project.ID, domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	if stored, _ := h.repo.AssistantSession(owner.ID, chat.ID); stored.Mode() != domain.AssistantModeWorkspace {
		t.Fatalf("fixture not in workspace mode: %q", stored.Mode())
	}
	h.generate = func(ctx context.Context, _ string, actor domain.User) (string, error) {
		var s *AgentToolSession
		agentToolSessions.mu.Lock()
		for _, candidate := range agentToolSessions.sessions {
			if candidate.ChatSessionID == chat.ID && candidate.UserID == actor.ID {
				s = candidate
			}
		}
		agentToolSessions.mu.Unlock()
		if s == nil || s.RuntimeMode != domain.AssistantModeWorkspace {
			return "", fmt.Errorf("turn did not start in the conversation's workspace mode")
		}
		g := &agentToolGateway{deps: h.deps}
		call := func(name string, args any) (map[string]any, error) {
			raw, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
			return g.callTool(withAgentToolToken(ctx, s.Token), s, raw)
		}
		for _, mode := range []string{AssistantApprovalAuto, AssistantApprovalAssisted, AssistantApprovalConfirm} {
			saved := s.ExecMode
			s.ExecMode = assistantExecModeForApproval(mode)
			for _, name := range []string{"agentflow__issues_save", "agentflow__issues_dispatch"} {
				if res, err := call(name, map[string]any{"body": map[string]any{"title": "x"}}); err == nil && res["isError"] != true {
					return "", fmt.Errorf("%s: direct platform write ran in workspace mode: %v", mode, res)
				}
			}
			s.ExecMode = saved
		}
		if res, err := call("workbench_tool_schema", map[string]any{"name": "issues_dispatch"}); err != nil || res["isError"] == true {
			return "", fmt.Errorf("tool schema unavailable in workspace: %v %v", res, err)
		}
		// The fixture has no platform API behind the gateway; the read only has
		// to pass the workspace catalog and reach dispatch.
		if res, err := call("agentflow__issues_list", map[string]any{"query": map[string]any{"projectId": project.ID}}); err != nil || strings.Contains(fmt.Sprint(res), errAgentToolForbidden.Error()) {
			return "", fmt.Errorf("platform read unavailable in workspace: %v %v", res, err)
		}
		res, err := call(submitAssistantReplyTool, map[string]any{
			"message": "请确认创建需求。",
			"drafts":  []map[string]any{{"tool": "issues_save", "body": map[string]any{"title": "导出时间按用户时区展示", "projectId": project.ID}}},
		})
		if err != nil || res["isError"] == true {
			return "", fmt.Errorf("draft submission refused in workspace: %v %v", res, err)
		}
		return "", nil
	}
	stored, _ := h.repo.AssistantSession(owner.ID, chat.ID)
	body, _ := json.Marshal(assistantRequest{ProjectID: project.ID, SessionID: chat.ID, Messages: append(stored.Messages, assistantMessage{Role: "user", Content: "@缺陷修复 SOP 派发时区缺陷"})})
	c, rec := workbenchContext(peer, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("turn failed: %d %s", rec.Code, rec.Body.String())
	}
	stored, _ = h.repo.AssistantSession(owner.ID, chat.ID)
	last := stored.Messages[len(stored.Messages)-1]
	var reply assistantReply
	if err := json.Unmarshal([]byte(last.Content), &reply); err != nil {
		t.Fatal(err)
	}
	if last.Role != "assistant" || len(reply.Drafts) != 1 || reply.Drafts[0].Tool != "issues_save" || reply.Drafts[0].Saved {
		t.Fatalf("draft was not kept for confirmation in this conversation: %s", last.Content)
	}
	if !strings.Contains(string(reply.Drafts[0].Body), project.ID) || stored.WorkspaceRunID != run.ID {
		t.Fatalf("draft lost project or conversation lost its workspace: %s %+v", reply.Drafts[0].Body, stored)
	}
}
