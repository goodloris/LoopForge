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
	// 完全权限 would apply a platform-mode draft on its own; from the
	// workspace it must still wait in this conversation for a person.
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAuto} {
		t.Run(mode, func(t *testing.T) { workspaceDraftConversation(t, mode) })
	}
}

func workspaceDraftConversation(t *testing.T, approvalMode string) {
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
	var turnErr error
	h.generate = func(ctx context.Context, prompt string, actor domain.User) (string, error) {
		out, err := workspaceDraftTurn(ctx, h, chat.ID, project.ID, actor)
		turnErr = err
		return out, err
	}
	stored, _ := h.repo.AssistantSession(owner.ID, chat.ID)
	body, _ := json.Marshal(assistantRequest{ProjectID: project.ID, SessionID: chat.ID, ApprovalMode: approvalMode, Messages: append(stored.Messages, assistantMessage{Role: "user", Content: "@缺陷修复 SOP 派发时区缺陷"})})
	c, rec := workbenchContext(peer, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if turnErr != nil || rec.Code != http.StatusOK {
		t.Fatalf("turn failed: %v (%d %s)", turnErr, rec.Code, rec.Body.String())
	}
	checkWorkspaceDraftStored(t, h, owner.ID, chat.ID, project.ID, run.ID)
}

// workspaceDraftTurn is the mock executor: it acts as the CLI of the turn.
func workspaceDraftTurn(ctx context.Context, h *assistantHandlers, chatID, projectID string, actor domain.User) (string, error) {
	var s *AgentToolSession
	agentToolSessions.mu.Lock()
	for _, candidate := range agentToolSessions.sessions {
		if candidate.ChatSessionID == chatID && candidate.UserID == actor.ID {
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
	if res, err := call("agentflow__issues_list", map[string]any{"query": map[string]any{"projectId": projectID}}); err != nil || strings.Contains(fmt.Sprint(res), errAgentToolForbidden.Error()) {
		return "", fmt.Errorf("platform read unavailable in workspace: %v %v", res, err)
	}
	res, err := call(submitAssistantReplyTool, map[string]any{
		"message": "请确认创建需求。",
		"drafts":  []map[string]any{{"tool": "issues_save", "body": map[string]any{"title": "导出时间按用户时区展示", "projectId": projectID}}},
	})
	if err != nil || res["isError"] == true {
		return "", fmt.Errorf("draft submission refused in workspace: %v %v", res, err)
	}
	return "", nil
}

func checkWorkspaceDraftStored(t *testing.T, h *assistantHandlers, owner int64, chatID, projectID, runID string) {
	t.Helper()
	stored, _ := h.repo.AssistantSession(owner, chatID)
	last := stored.Messages[len(stored.Messages)-1]
	var reply assistantReply
	if err := json.Unmarshal([]byte(last.Content), &reply); err != nil {
		t.Fatal(err)
	}
	if last.Role != "assistant" || len(reply.Drafts) != 1 || reply.Drafts[0].Tool != "issues_save" || reply.Drafts[0].Saved || reply.Drafts[0].Auto {
		t.Fatalf("draft was not kept for confirmation in this conversation: %s", last.Content)
	}
	if !strings.Contains(string(reply.Drafts[0].Body), projectID) || stored.WorkspaceRunID != runID {
		t.Fatalf("draft lost project or conversation lost its workspace: %s %+v", reply.Drafts[0].Body, stored)
	}
}

// A draft submitted from a workspace phase waits for a person in every
// approval mode: repository code there may have injected it. Assisted mode
// still shows the review, as a reference only.
func TestWorkspaceDraftsAlwaysNeedHumanConfirmation(t *testing.T) {
	h, _ := workbenchFixture(t)
	draft := assistantDraft{Tool: "agents_save", Body: json.RawMessage(`{"id":"a","name":"A","projectId":"workbench"}`)}
	ctx := withAssistantApprovalReviewer(context.Background(), func(context.Context, string, string, map[string]any) assistantApprovalReview {
		return assistantApprovalReview{Outcome: "allow", RiskLevel: "low", UserAuthorization: "high", Rationale: "用户要求创建角色"}
	})
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		// The same draft from platform mode runs on its own outside confirm mode.
		platform := h.resolveAssistantDraftsFor(ctx, domain.User{}, "workbench", []assistantDraft{draft}, mode, false)
		if platform[0].Auto != (mode != AssistantApprovalConfirm) {
			t.Fatalf("%s: platform-mode baseline changed: %+v", mode, platform[0])
		}
		out := h.resolveAssistantDraftsFor(ctx, domain.User{}, "workbench", []assistantDraft{draft}, mode, true)
		if len(out) != 1 || out[0].Auto || out[0].Saved {
			t.Fatalf("%s: workspace draft ran without a person: %+v", mode, out)
		}
		if (out[0].Review != nil) != (mode == AssistantApprovalAssisted) || mode == AssistantApprovalAssisted && out[0].Review.Rationale != "用户要求创建角色" {
			t.Fatalf("%s: review shown as reference = %+v", mode, out[0].Review)
		}
	}
	if _, ok := h.repo.Agent("a"); ok {
		t.Fatal("a workspace draft was applied")
	}
}
