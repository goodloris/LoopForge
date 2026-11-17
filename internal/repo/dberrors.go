package repo

import (
	"errors"
	"strconv"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// Database error classification. Code branches on what an error means, never
// on a dialect's error number, so each meaning lists its MySQL numbers and
// PostgreSQL SQLSTATEs here.

func mysqlErrorNumber(err error) uint16 {
	var e *mysqldriver.MySQLError
	if errors.As(err, &e) {
		return e.Number
	}
	return 0
}

func pgSQLState(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// IsDuplicateKey reports a unique-key violation: MySQL 1062, PostgreSQL
// 23505, or gorm's translated ErrDuplicatedKey (sqlite, and any dialect with
// TranslateError).
func IsDuplicateKey(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || mysqlErrorNumber(err) == 1062 || pgSQLState(err) == "23505"
}

// isRetryableConflict reports a transaction the server rolled back as a
// whole because of a concurrent one, so replaying it is safe: MySQL deadlock
// 1213, PostgreSQL deadlock_detected 40P01 and serialization_failure 40001.
// Lock wait timeouts (MySQL 1205, PostgreSQL 55P03) are not included: MySQL
// rolls back only the statement there.
func isRetryableConflict(err error) bool {
	if mysqlErrorNumber(err) == 1213 {
		return true
	}
	switch pgSQLState(err) {
	case "40P01", "40001":
		return true
	}
	return false
}

// isLockTimeout reports PostgreSQL lock_not_available (lock_timeout expired).
func isLockTimeout(err error) bool {
	return pgSQLState(err) == "55P03"
}

// summaryProjectionUnsupported reports a server that cannot parse the
// projection at all, as opposed to a transient failure: MySQL 1064 (syntax,
// no JSON_TABLE) / 1305 (missing function), PostgreSQL 42601 / 42883.
func summaryProjectionUnsupported(err error) bool {
	switch mysqlErrorNumber(err) {
	case 1064, 1305:
		return true
	}
	switch pgSQLState(err) {
	case "42601", "42883":
		return true
	}
	return false
}

// UnstorableTextCode names the error when the database cannot store some
// characters of a text value: MySQL 1366/3988 (a utf8mb3 table and an emoji)
// or PostgreSQL 22021/22P05 (a database encoding other than UTF8). It is ""
// for any other error.
func UnstorableTextCode(err error) string {
	switch n := mysqlErrorNumber(err); n {
	case 1366, 3988:
		return "MySQL " + strconv.Itoa(int(n))
	}
	switch code := pgSQLState(err); code {
	case "22021", "22P05":
		return "PostgreSQL " + code
	}
	return ""
}
