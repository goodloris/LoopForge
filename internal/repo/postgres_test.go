package repo

import (
	"errors"
	"testing"

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
