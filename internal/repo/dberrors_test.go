package repo

import (
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestDatabaseErrorClassification(t *testing.T) {
	my := func(n uint16) error { return fmt.Errorf("wrapped: %w", &mysqldriver.MySQLError{Number: n}) }
	pg := func(code string) error { return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code}) }
	for _, tc := range []struct {
		name string
		fn   func(error) bool
		yes  []error
		no   []error
	}{
		{"IsDuplicateKey", IsDuplicateKey,
			[]error{my(1062), pg("23505"), fmt.Errorf("x: %w", gorm.ErrDuplicatedKey)},
			[]error{my(1213), pg("23503"), nil}},
		{"isRetryableConflict", isRetryableConflict,
			[]error{my(1213), pg("40P01"), pg("40001")},
			[]error{my(1205), pg("55P03"), pg("23505"), nil}},
		{"summaryProjectionUnsupported", summaryProjectionUnsupported,
			[]error{my(1064), my(1305), pg("42601"), pg("42883")},
			[]error{my(1205), pg("40P01"), fmt.Errorf("driver: bad connection"), nil}},
		{"isLockTimeout", isLockTimeout, []error{pg("55P03")}, []error{my(1205), nil}},
	} {
		for _, err := range tc.yes {
			if !tc.fn(err) {
				t.Errorf("%s(%v) = false, want true", tc.name, err)
			}
		}
		for _, err := range tc.no {
			if tc.fn(err) {
				t.Errorf("%s(%v) = true, want false", tc.name, err)
			}
		}
	}
	for err, want := range map[error]string{my(1366): "MySQL 1366", my(3988): "MySQL 3988", pg("22021"): "PostgreSQL 22021", pg("22P05"): "PostgreSQL 22P05", my(1062): "", pg("23505"): ""} {
		if got := UnstorableTextCode(err); got != want {
			t.Errorf("UnstorableTextCode(%v) = %q, want %q", err, got, want)
		}
	}
}
