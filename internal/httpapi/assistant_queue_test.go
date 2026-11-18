package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// D7: a message one member queues behind the running reply is visible to the
// other members, with its sender, until the next turn starts.
func TestQueuedMessagesVisibleToOtherMembers(t *testing.T) {
	h, owner, peer, outsider, conv := sharedWorkbenchFixture(t)
	call := func(u domain.User, method, body string) (int, []domain.AssistantQueuedMessage) {
		c, rec := workbenchContext(u, method, "/", bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "id", Value: conv.ID}}
		h.queue(c)
		var out []domain.AssistantQueuedMessage
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// Nothing is running: nothing to show, nothing stored.
	if code, out := call(peer, http.MethodPut, `{"items":[{"id":"q0","text":"早了"}]}`); code != 200 || len(out) != 0 {
		t.Fatalf("idle queue = %d %+v", code, out)
	}
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "queue-turn", UserID: owner.ID, SessionID: conv.ID, ActorID: owner.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(peer, http.MethodPut, `{"items":[{"id":"q1","text":"时间按用户所在时区展示","attachments":1}]}`); code != 200 {
		t.Fatalf("queue preview rejected: %d", code)
	}
	code, out := call(owner, http.MethodGet, "")
	if code != 200 || len(out) != 1 || out[0].AuthorID != peer.ID || out[0].AuthorName != peer.LoginName || out[0].Text != "时间按用户所在时区展示" || out[0].Attachments != 1 {
		t.Fatalf("other member sees %d %+v", code, out)
	}
	// A forged author is ignored: previews always belong to the caller.
	call(owner, http.MethodPut, `{"items":[{"id":"q2","text":"mine","authorId":999}]}`)
	if _, out = call(peer, http.MethodGet, ""); len(out) != 2 || out[1].AuthorID != owner.ID {
		t.Fatalf("previews = %+v", out)
	}
	call(peer, http.MethodPut, `{"items":[]}`)
	if _, out = call(owner, http.MethodGet, ""); len(out) != 1 || out[0].AuthorID != owner.ID {
		t.Fatalf("sent message still shown: %+v", out)
	}
	if code, _ := call(outsider, http.MethodGet, ""); code != http.StatusForbidden {
		t.Fatalf("outsider read the queue: %d", code)
	}
	if err := h.repo.CheckpointAssistantGeneration(domain.AssistantGeneration{ID: "queue-turn", UserID: owner.ID, SessionID: conv.ID, ActorID: owner.ID, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, out = call(peer, http.MethodGet, ""); len(out) != 0 {
		t.Fatalf("previews outlived the turn: %+v", out)
	}
}
