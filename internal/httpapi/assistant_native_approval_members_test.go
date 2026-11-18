package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// D6: a pending external write is visible to every member of the
// conversation; members other than the speaker may answer it, viewers may
// not, and the call keeps running as the speaker.
func TestNativeApprovalVisibleToMembersAndDecidedByWriters(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, actor, peer, _, conv := sharedWorkbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "approval-viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := h.repo.UserByLoginName("approval-viewer")
	project, _ := h.repo.Project(conv.ProjectID)
	project.Members = append(project.Members, domain.ProjectMember{UserID: viewer.ID, Role: "viewer"})
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "member-approval", UserID: actor.ID, SessionID: conv.ID, ActorID: actor.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{GenerationID: g.ID})
	var notes []string
	s := &AgentToolSession{Token: "member-phase", UserID: actor.ID, User: actor, ChatOwnerID: actor.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModeWorkspace, ctx: ctx,
		ExecMode: assistantExecModeForApproval(AssistantApprovalConfirm),
		Notify: func(kind string, value any) {
			if ev, ok := value.(assistantStreamEvent); ok && kind == "thought" {
				notes = append(notes, ev.Text)
			}
		},
	}
	gateway := &agentToolGateway{deps: h.deps}
	push := json.RawMessage(`{"id":"push","name":"Bash","input":{"command":"git push origin fix"}}`)
	if result := gateway.approveNativeTool(ctx, s, push); !result.Pending {
		t.Fatalf("confirm mode must wait: %+v", result)
	}

	list := func(u domain.User) []map[string]any {
		c, rec := workbenchContext(u, http.MethodGet, "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: conv.ID}}
		h.nativeApprovals(c)
		var out []map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list as %s: %d %s", u.LoginName, rec.Code, rec.Body.String())
		}
		return out
	}
	decide := func(u domain.User, id, decision string) int {
		c, rec := workbenchContext(u, http.MethodPost, "/", bytes.NewBufferString(`{"decision":"`+decision+`"}`))
		c.Params = gin.Params{{Key: "id", Value: conv.ID}, {Key: "approvalId", Value: id}}
		h.nativeApprovals(c)
		return rec.Code
	}
	for _, u := range []domain.User{peer, viewer} {
		items := list(u)
		if len(items) != 1 || items[0]["actorName"] != actor.LoginName || items[0]["canDecide"] != (u.ID != viewer.ID) {
			t.Fatalf("%s sees %+v", u.LoginName, items)
		}
	}
	id := list(peer)[0]["id"].(string)
	if code := decide(viewer, id, "allow"); code != http.StatusForbidden {
		t.Fatalf("viewer answered an external write: %d", code)
	}
	if item, _ := h.repo.NativeApproval(actor.ID, conv.ID, id); item.Decision != "pending" {
		t.Fatalf("viewer decision was stored: %+v", item)
	}
	if code := decide(peer, id, "allow"); code != http.StatusOK {
		t.Fatalf("member could not approve for the speaker: %d", code)
	}
	if item, _ := h.repo.NativeApproval(actor.ID, conv.ID, id); item.DecidedBy != peer.ID || item.ActorID != actor.ID {
		t.Fatalf("decision record = %+v", item)
	}
	if result := gateway.approveNativeTool(ctx, s, push); !result.Allow {
		t.Fatalf("approved call did not run: %+v", result)
	}
	gateway.approveNativeTool(ctx, s, push)
	// The command runs in the speaker's CLI phase, which carries the speaker's
	// identity and Git credentials; approving never rebinds it.
	if s.UserID != actor.ID || s.User.ID != actor.ID {
		t.Fatalf("approval changed the executing identity: %d", s.UserID)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], peer.LoginName+" 批准") || !strings.Contains(notes[0], "代 "+actor.LoginName) {
		t.Fatalf("activity must record who approved once: %q", notes)
	}
}
