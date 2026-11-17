package repo

import (
	"errors"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// retryAssistantDeadlock guards PutAssistantSession and BeginAssistantGeneration.
// The MySQL concurrency test needs a disposable Docker MySQL and is skipped in
// CI, so the retry contract is pinned here with injected driver errors.
func TestRetryAssistantDeadlockRetriesOnlyDeadlocks(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadlock := &mysqldriver.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}

	calls := 0
	for _, victim := range []error{deadlock, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "40001"}} {
		calls = 0
		if err := s.retryAssistantDeadlock(func() error {
			calls++
			if calls < 3 {
				return victim
			}
			return nil
		}); err != nil || calls != 3 {
			t.Fatalf("%v victim should be retried until it commits: err=%v calls=%d", victim, err, calls)
		}
	}

	calls = 0
	if err := s.retryAssistantDeadlock(func() error {
		calls++
		return deadlock
	}); !errors.Is(err, deadlock) || calls != 6 {
		t.Fatalf("persistent deadlock must stop after 6 attempts and surface the driver error: err=%v calls=%d", err, calls)
	}

	for _, failure := range []error{
		&mysqldriver.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"},
		&pgconn.PgError{Code: "55P03"},
		&pgconn.PgError{Code: "23505"},
		errors.New("connection reset"),
	} {
		calls = 0
		if err := s.retryAssistantDeadlock(func() error {
			calls++
			return failure
		}); !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("%v must not be replayed: err=%v calls=%d", failure, err, calls)
		}
	}

	calls = 0
	if err := s.retryAssistantDeadlock(func() error {
		calls++
		return nil
	}); err != nil || calls != 1 {
		t.Fatalf("successful transaction must run once: err=%v calls=%d", err, calls)
	}
}
