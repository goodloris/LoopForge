package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"slices"
)

// SetQueuedMessages replaces author's queued previews on the running turn
// of session. Like native approvals they live in the generation payload, so
// every API instance serves the same list and a new turn starts empty.
func (r *AssistantRepo) SetQueuedMessages(owner int64, session string, author int64, items []domain.AssistantQueuedMessage) error {
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", session, owner).Take(&row).Error; err != nil || !r.nativeApprovalGenerationActive(r.DB, row) {
		return fmt.Errorf("当前没有进行中的回复")
	}
	return r.updateNativeApprovals(owner, session, row.GenerationID, true, func(payload *assistantGenerationPayload) error {
		payload.Queued = slices.DeleteFunc(payload.Queued, func(item domain.AssistantQueuedMessage) bool { return item.AuthorID == author })
		for _, item := range items {
			item.AuthorID = author
			payload.Queued = append(payload.Queued, item)
		}
		return nil
	})
}

// QueuedMessages lists the previews queued behind the running turn.
func (r *AssistantRepo) QueuedMessages(owner int64, session string) []domain.AssistantQueuedMessage {
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", session, owner).Take(&row).Error; err != nil || !r.nativeApprovalGenerationActive(r.DB, row) {
		return []domain.AssistantQueuedMessage{}
	}
	return append([]domain.AssistantQueuedMessage{}, generationPayload(row).Queued...)
}
