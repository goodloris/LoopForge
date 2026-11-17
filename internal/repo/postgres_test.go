package repo

import (
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
)

// openPostgresTest returns an OpenTest store, skipping unless it is backed by
// PostgreSQL (AGENTFLOW_TEST_PG_DSN).
func openPostgresTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if !s.isPostgres() {
		t.Skip("AGENTFLOW_TEST_PG_DSN not set")
	}
	return s
}

func TestPostgresLoginNamesAreCaseInsensitive(t *testing.T) {
	s := openPostgresTest(t)
	if err := s.PutUser(domain.User{LoginName: "Alice", ChineseName: "A"}); err != nil {
		t.Fatal(err)
	}
	first, ok := s.UserByLoginName("alice")
	if !ok {
		t.Fatal("lookup by a different case missed the user")
	}
	// A login differing only in case adopts the existing row.
	if err := s.PutUser(domain.User{LoginName: "ALICE", ChineseName: "B"}); err != nil {
		t.Fatal(err)
	}
	var n int64
	s.DB.Table("users").Where("lower(login_name) = 'alice'").Count(&n)
	if n != 1 {
		t.Fatalf("users matching alice = %d, want 1", n)
	}
	again, _ := s.UserByLoginName("Alice")
	if again.ID != first.ID || again.ChineseName != "B" {
		t.Fatalf("second write = %+v, want id %d refreshed", again, first.ID)
	}
	for table, index := range map[string]string{"users": "idx_users_login_name_lower", "business_domains": "idx_business_domains_name_lower"} {
		var def string
		s.DB.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?", table, index).Scan(&def)
		if !strings.Contains(def, "UNIQUE") || !strings.Contains(def, "lower(") {
			t.Fatalf("%s index %s = %q, want a unique lower() index", table, index, def)
		}
	}
	// The index itself rejects a case variant, whatever path writes it.
	err := s.DB.Exec("INSERT INTO users (login_name) VALUES ('aLiCe')").Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("raw case-variant insert err = %v, want duplicate key", err)
	}
}

func TestPostgresBusinessDomainNamesAreCaseInsensitive(t *testing.T) {
	s := openPostgresTest(t)
	if err := s.PutBusinessDomain(domain.BusinessDomain{ID: "bd-1", Name: "Payments", Active: true}, nil); err != nil {
		t.Fatal(err)
	}
	err := s.PutBusinessDomain(domain.BusinessDomain{ID: "bd-2", Name: "payments", Active: true}, nil)
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("case-variant name err = %v, want duplicate key", err)
	}
}

// A real PostgreSQL unique violation, read through database/sql so gorm's
// translation cannot mask it, is classified as a duplicate key.
func TestPostgresRawUniqueViolationIsDuplicateKey(t *testing.T) {
	s := openPostgresTest(t)
	sqlDB, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("INSERT INTO users (login_name) VALUES ('dup')"); err != nil {
		t.Fatal(err)
	}
	_, err = sqlDB.Exec("INSERT INTO users (login_name) VALUES ('dup')")
	if err == nil || !IsDuplicateKey(err) {
		t.Fatalf("raw duplicate err = %v, want IsDuplicateKey", err)
	}
}

// pgx would return timestamptz in time.Local; rows must read back in UTC so
// they serialize with Z like MySQL's loc=UTC.
func TestPostgresTimesReadBackInUTC(t *testing.T) {
	s := openPostgresTest(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("CST", 8*3600))
	if err := s.PutRun(domain.Run{ID: "utc-run", Status: "running", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	var row runRow
	if err := s.DB.Where("id = ?", "utc-run").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Created == nil || row.Created.Location() != time.UTC || !row.Created.Equal(at) {
		t.Fatalf("created_at = %v (location %v), want %v in UTC", row.Created, row.Created.Location(), at)
	}
	var now time.Time
	if err := s.DB.Raw("SELECT statement_timestamp()").Scan(&now).Error; err != nil {
		t.Fatal(err)
	}
	if now.Location() != time.UTC {
		t.Fatalf("raw timestamptz location = %v, want UTC", now.Location())
	}
}

// NUL in text columns and JSON payloads is stored as the visible \u0000
// marker instead of failing the write or vanishing.
func TestPostgresNULIsEscapedNotRejected(t *testing.T) {
	s := openPostgresTest(t)
	run := domain.Run{ID: "nul-run", Status: "running", Input: "in\x00put", Nodes: map[string]*domain.NodeRun{
		"n": {NodeID: "n", Status: "succeeded", Output: "out\x00put"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatalf("write with NUL: %v", err)
	}
	got, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if got.Input != `in\u0000put` || got.Nodes["n"].Output != `out\u0000put` {
		t.Fatalf("input=%q output=%q, want the \\u0000 marker in place of NUL", got.Input, got.Nodes["n"].Output)
	}
	session := domain.AssistantSession{ID: "nul-chat", Messages: []domain.AssistantMessage{{Role: "assistant", Content: "a\x00b"}}}
	if err := s.PutAssistantSession(1, session); err != nil {
		t.Fatalf("session with NUL: %v", err)
	}
	saved, _ := s.AssistantSession(1, "nul-chat")
	if len(saved.Messages) != 1 || saved.Messages[0].Content != `a\u0000b` {
		t.Fatalf("session messages = %+v", saved.Messages)
	}
	// bytea is binary and must stay byte-exact, including NUL and text that
	// looks like a JSON NUL escape.
	blob := []byte("a\x00b\\u0000c")
	if err := s.DB.Create(&previewBlobRow{SHA256: "nul-blob", Content: blob}).Error; err != nil {
		t.Fatal(err)
	}
	var back previewBlobRow
	if err := s.DB.Where("sha256 = ?", "nul-blob").Take(&back).Error; err != nil || string(back.Content) != string(blob) {
		t.Fatalf("bytea = %q (%v), want %q", back.Content, err, blob)
	}
}
