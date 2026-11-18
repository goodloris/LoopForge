package repo

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// The first message to a new conversation, sent at once from several API
// replicas (double submit, two tabs), starts exactly one turn. Every other
// sender gets the answer it would get a moment later: the conversation is
// already generating, or it changed. MySQL gets there by retrying the gap-lock
// deadlock; PostgreSQL has no gap locks, so the losers collide on the insert
// and must be retried the same way instead of surfacing the unique violation
// (an HTTP 409 carrying raw SQL).
//
// Chat passes the pending session. On PostgreSQL its upsert already waits for
// the first writer's commit, so the race shows only on the generation insert
// of a caller that passes none.
func TestConcurrentFirstMessagesStartOneGeneration(t *testing.T) {
	for _, withSession := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending_session=%v", withSession), func(t *testing.T) {
			concurrentFirstMessages(t, withSession)
		})
	}
}

func concurrentFirstMessages(t *testing.T, withSession bool) {
	const writers = 8
	const owner = 51
	s := serverTestStore(t)
	stores := []*Store{s}
	for len(stores) < writers {
		stores = append(stores, serverTestPeer(t, s))
	}
	session := fmt.Sprintf("first-message-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		s.DB.Where("session_id = ?", session).Delete(&assistantGenerationRow{})
		s.DB.Where("id = ?", session).Delete(&assistantSessionRow{})
	})

	// Hold every writer after its locking read found the session missing
	// until all of them have, so all of them go on to create it.
	entered, release := make(chan struct{}, writers), make(chan struct{})
	var arrivals atomic.Int32
	const callback = "test:first_message_race"
	for _, st := range stores {
		if err := st.DB.Callback().Query().After("gorm:query").Register(callback, func(db *gorm.DB) {
			if db.Statement.Table != "assistant_sessions" || !errors.Is(db.Error, gorm.ErrRecordNotFound) {
				return
			}
			if _, locked := db.Statement.Clauses["FOR"]; locked && arrivals.Add(1) <= writers {
				entered <- struct{}{}
				<-release
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, writers)
	for i, st := range stores {
		go func() {
			pending := domain.AssistantSession{ID: session, Title: "并发首条", Messages: []domain.AssistantMessage{{Role: "user", Content: "并发首条", AuthorID: owner}}}
			g := domain.AssistantGeneration{ID: fmt.Sprintf("%s-%d", session, i), SessionID: session, UserID: owner, ActorID: owner, Status: "running"}
			if !withSession {
				results <- st.BeginAssistantGeneration(g)
				return
			}
			results <- st.BeginAssistantGeneration(g, pending)
		}()
	}
	for i := 0; i < writers; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Errorf("only %d of %d writers found the session missing", i, writers)
			i = writers
		}
	}
	close(release)
	wins := 0
	for i := 0; i < writers; i++ {
		select {
		case err := <-results:
			switch {
			case err == nil:
				wins++
			case err.Error() == "此会话正在生成，请等待完成或停止", err.Error() == "会话已更新，请重新打开后发送":
			default:
				t.Errorf("a concurrent first message failed with %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("concurrent first messages did not finish")
		}
	}
	if wins != 1 {
		t.Fatalf("%d first messages started a turn, want exactly 1", wins)
	}
	g, ok := s.PeekAssistantGeneration(owner, session)
	if !ok || g.Status != "running" {
		t.Fatalf("the winning turn is not running: %+v %v", g, ok)
	}
	if !withSession {
		return
	}
	if saved, ok := s.AssistantSession(owner, session); !ok || len(saved.Messages) != 1 || !saved.ExecutionTargetLocked {
		t.Fatalf("the winning turn's session was not stored: %+v %v", saved, ok)
	}
}
