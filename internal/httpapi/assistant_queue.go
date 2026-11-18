package httpapi

import (
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

const (
	assistantQueueMaxItems = 20
	assistantQueueMaxText  = 500
)

// queue handles GET/PUT /api/assistant/sessions/:id/queue: previews of the
// messages members queued behind the running reply, so everyone in a shared
// conversation sees who is waiting with what. PUT replaces the caller's own
// previews; the messages themselves are sent by the caller's browser.
func (h *assistantHandlers) queue(c *gin.Context) {
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(401, gin.H{"error": "请先登录"})
		return
	}
	owner, _, err := h.conversationScope(c, c.Param("id"), "", "", user.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": err.Error()})
		return
	}
	if c.Request.Method == "GET" {
		c.JSON(200, h.repo.QueuedMessages(owner, c.Param("id")))
		return
	}
	var body struct {
		Items []struct {
			ID          string `json:"id"`
			Text        string `json:"text"`
			Attachments int    `json:"attachments"`
		} `json:"items"`
	}
	if !decode(c, &body) {
		return
	}
	if len(body.Items) > assistantQueueMaxItems {
		body.Items = body.Items[:assistantQueueMaxItems]
	}
	now := time.Now().UTC()
	items := make([]domain.AssistantQueuedMessage, 0, len(body.Items))
	for _, item := range body.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 64 {
			continue
		}
		text := strings.TrimSpace(item.Text)
		if utf8.RuneCountInString(text) > assistantQueueMaxText {
			text = string([]rune(text)[:assistantQueueMaxText]) + "…"
		}
		items = append(items, domain.AssistantQueuedMessage{ID: id, AuthorName: user.LoginName, Text: text, Attachments: max(0, item.Attachments), QueuedAt: now})
	}
	// Nothing is running: the queue goes out on its own, nothing to show.
	if err := h.repo.SetQueuedMessages(owner, c.Param("id"), user.ID, items); err != nil {
		c.JSON(200, []domain.AssistantQueuedMessage{})
		return
	}
	c.JSON(200, h.repo.QueuedMessages(owner, c.Param("id")))
}
