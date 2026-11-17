package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestEmojiPersistenceFailureFinishesWithActionableReply(t *testing.T) {
	for _, tc := range []struct {
		err        error
		code, hint string
	}{
		{&mysql.MySQLError{Number: 1366, Message: "Incorrect string value"}, "1366", "utf8mb4"},
		{&mysql.MySQLError{Number: 3988, Message: "Incorrect string value"}, "3988", "utf8mb4"},
		{&pgconn.PgError{Code: "22021", Message: "character not in repertoire"}, "22021", "UTF8"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			item := domain.BotInteraction{ID: "emoji-reply", BotID: "bot", Status: "running"}
			if err := store.DB.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			failures := 0
			if err := store.DB.Callback().Update().Before("gorm:update").Register("test_utf8mb3", func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]any)
				if !ok {
					return
				}
				if text, ok := updates["reply"].(string); ok && strings.Contains(text, "👋") {
					failures++
					tx.AddError(tc.err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			service := New(store)
			reply, detail, err := service.saveReply(item, "你好 👋", "")
			if err != nil || failures != 1 || !strings.Contains(reply, tc.hint) || !strings.Contains(detail, tc.code) {
				t.Fatal(reply, detail, err, failures)
			}
			saved, err := store.BotInteraction(item.ID)
			if err != nil || saved.Status != "ready" || saved.Reply != reply || saved.LastError != detail {
				t.Fatal(saved, err)
			}

		})
	}
}
