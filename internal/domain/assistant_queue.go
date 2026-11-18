package domain

import "time"

// AssistantQueuedMessage previews a message a member typed while a reply
// was running. The message itself stays in that member's browser until it
// is sent; the preview only lets the others see it waiting.
type AssistantQueuedMessage struct {
	ID          string    `json:"id"`
	AuthorID    int64     `json:"authorId"`
	AuthorName  string    `json:"authorName,omitempty"`
	Text        string    `json:"text"`
	Attachments int       `json:"attachments,omitempty"`
	QueuedAt    time.Time `json:"queuedAt"`
}
