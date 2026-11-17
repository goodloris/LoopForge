package repo

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Runs only when OpenTest is backed by PostgreSQL (AGENTFLOW_TEST_PG_DSN).
func TestLockIntegrationPostgresAdvisoryLock(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.isPostgres() {
		t.Skip("AGENTFLOW_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	release, err := s.LockIntegration(ctx, "repo-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LockIntegration(ctx, "repo-a", 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("second holder err = %v, want timeout", err)
	}
	other, err := s.LockIntegration(ctx, "repo-b", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unrelated repository blocked: %v", err)
	}
	other()
	release()
	again, err := s.LockIntegration(ctx, "repo-a", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	again()
}
