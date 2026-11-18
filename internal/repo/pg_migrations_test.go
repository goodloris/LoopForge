package repo

import (
	"agentflow/internal/domain"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The PostgreSQL seed under migrations/postgres/seed is a hand-kept copy of
// the MySQL seed. These tests fail when one side changes without the other.

const (
	mysqlSeedDir = "../../migrations/seed/"
	pgMigrations = "../../migrations/postgres/"
)

var seedInsertLine = regexp.MustCompile(`^INSERT (?:IGNORE )?INTO (\w+) \(([^)]*)\) VALUES \((.*)\)(?: ON CONFLICT DO NOTHING)?;$`)

// seedRows parses the single-line INSERT ... VALUES (...) statements of a
// seed file into rows keyed "table/id". mysql selects MySQL string escapes
// (backslash sequences) over PostgreSQL standard-conforming literals.
func seedRows(t *testing.T, path string, mysql bool) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]any{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "INSERT") {
			continue
		}
		m := seedInsertLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: unparsed INSERT: %.120s", path, line)
		}
		cols := strings.Split(strings.ReplaceAll(m[2], "`", ""), ",")
		vals := sqlValues(t, m[3], mysql)
		if len(cols) != len(vals) {
			t.Fatalf("%s: %s has %d columns and %d values", path, m[1], len(cols), len(vals))
		}
		row := map[string]any{}
		for i, col := range cols {
			v := vals[i]
			if col == "payload" {
				var decoded any
				if err := json.Unmarshal([]byte(v.(string)), &decoded); err != nil {
					t.Fatalf("%s: %s payload is not JSON: %v", path, m[1], err)
				}
				v = decoded
			}
			row[col] = v
		}
		key := m[1] + "/" + row["id"].(string)
		if _, dup := rows[key]; dup {
			t.Fatalf("%s: duplicate row %s", path, key)
		}
		rows[key] = row
	}
	return rows
}

// sqlValues splits a VALUES tuple into string literals and bare tokens.
// Both dialects' "current time" functions become "<now>".
func sqlValues(t *testing.T, s string, mysql bool) []any {
	t.Helper()
	var out []any
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ',' || c == ' ':
			i++
		case c == '\'':
			var b strings.Builder
			for i++; ; i++ {
				if i >= len(s) {
					t.Fatalf("unterminated string literal")
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						b.WriteByte('\'')
						i++
						continue
					}
					i++
					break
				}
				if mysql && s[i] == '\\' && i+1 < len(s) {
					i++
					if r, ok := map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', '0': 0}[s[i]]; ok {
						b.WriteByte(r)
					} else {
						b.WriteByte(s[i]) // \\, \', \"
					}
					continue
				}
				b.WriteByte(s[i])
			}
			out = append(out, b.String())
		default:
			end := strings.IndexByte(s[i:], ',')
			if end < 0 {
				end = len(s) - i
			}
			tok := strings.TrimSpace(s[i : i+end])
			if tok == "UTC_TIMESTAMP()" || tok == "now()" {
				tok = "<now>"
			}
			out = append(out, tok)
			i += end
		}
	}
	return out
}

func TestPostgresSeedMatchesMySQLSeed(t *testing.T) {
	mysql := seedRows(t, mysqlSeedDir+"001_public_catalog.sql", true)
	pg := seedRows(t, pgMigrations+"seed/001_public_catalog.sql", false)
	if len(mysql) == 0 {
		t.Fatal("MySQL seed has no rows")
	}
	keys := func(m map[string]map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if !reflect.DeepEqual(keys(mysql), keys(pg)) {
		t.Fatalf("seed rows differ:\nmysql %v\npg    %v", keys(mysql), keys(pg))
	}
	for key, want := range mysql {
		if got := pg[key]; !reflect.DeepEqual(want, got) {
			t.Errorf("%s differs between the MySQL and PostgreSQL seed", key)
		}
	}
	// The parser decodes MySQL escapes; prove it on a value that has them.
	content, _ := pg["dockerfiles/generic-backend"]["payload"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "\nWORKDIR /workspace\n") {
		t.Errorf("generic-backend Dockerfile lost its newlines: %q", content)
	}

	welcome := regexp.MustCompile(`'(\{"id": "welcome"[^']*)'`)
	project := func(path string) any {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := welcome.FindSubmatch(raw)
		if m == nil {
			t.Fatalf("%s: welcome project payload missing", path)
		}
		var v any
		if err := json.Unmarshal(m[1], &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if want, got := project(mysqlSeedDir+"002_welcome_project.sql"), project(pgMigrations+"seed/002_welcome_project.sql"); !reflect.DeepEqual(want, got) {
		t.Errorf("welcome project differs:\nmysql %v\npg    %v", want, got)
	}
}

// TestPostgresBaselineAndSeed applies migrations/postgres in README order on
// a gorm-built schema, twice, and checks what the scripts promise.
func TestPostgresBaselineAndSeed(t *testing.T) {
	baseDSN := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_PG_DSN"))
	if baseDSN == "" {
		t.Skip("AGENTFLOW_TEST_PG_DSN not set")
	}
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("%v\n%.200s", err, query)
		}
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%v: %s", err, query)
		}
		return n
	}
	file := func(name string) string {
		raw, err := os.ReadFile(pgMigrations + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// OpenTest writes its own catalog defaults; start from the empty tables
	// a fresh install has.
	want := map[string]int{"projects": 1}
	for key := range seedRows(t, mysqlSeedDir+"001_public_catalog.sql", true) {
		want[strings.SplitN(key, "/", 2)[0]]++
	}
	for table := range want {
		exec("DELETE FROM " + table)
	}
	exec("DELETE FROM project_members; DELETE FROM users")

	for round := 1; round <= 2; round++ {
		for _, name := range []string{"001_baseline_extensions.sql", "seed/001_public_catalog.sql", "seed/002_welcome_project.sql"} {
			exec(file(name))
		}
		for table, n := range want {
			if got := count("SELECT count(*) FROM " + table); got != n {
				t.Errorf("round %d: %s has %d rows, want %d", round, table, got, n)
			}
			if got := count("SELECT count(*) FROM " + table + " WHERE jsonb_typeof(payload::jsonb) = 'object'"); got != n {
				t.Errorf("round %d: %s payloads that jsonb accepts as objects: %d, want %d", round, table, got, n)
			}
		}
	}

	var owner sql.NullInt64
	var payload string
	if err := db.QueryRow("SELECT owner_user_id, payload::text FROM projects WHERE id='welcome'").Scan(&owner, &payload); err != nil {
		t.Fatal(err)
	}
	var project domain.Project
	if err := json.Unmarshal([]byte(payload), &project); err != nil {
		t.Fatal(err)
	}
	if owner.Valid || project.OwnerID != 0 || len(project.Members) != 0 || project.WorkflowID != "welcome-workflow" {
		t.Errorf("welcome project without admin: owner=%v payload=%s", owner, payload)
	}
	// With an admin account (matched case-insensitively, as MySQL does) the
	// project is owned by it.
	exec("DELETE FROM projects; INSERT INTO users (login_name) VALUES ('Admin')")
	exec(file("seed/002_welcome_project.sql"))
	adminID := count("SELECT id FROM users WHERE login_name='Admin'")
	if err := db.QueryRow("SELECT owner_user_id, payload::text FROM projects WHERE id='welcome'").Scan(&owner, &payload); err != nil {
		t.Fatal(err)
	}
	project = domain.Project{}
	if err := json.Unmarshal([]byte(payload), &project); err != nil {
		t.Fatal(err)
	}
	if !owner.Valid || owner.Int64 != int64(adminID) || project.OwnerID != int64(adminID) || len(project.Members) != 1 || project.Members[0].UserID != int64(adminID) {
		t.Errorf("welcome project with admin %d: owner=%v payload=%s", adminID, owner, payload)
	}
	if got := count("SELECT count(*) FROM project_members WHERE project_id='welcome' AND role='maintainer'"); got != 1 {
		t.Errorf("welcome maintainers = %d, want 1", got)
	}

	checkGeneratedColumns(t, db)

	// A restart after the baseline runs production Open's incremental DDL,
	// which must accept the extra columns and indexes.
	var schema string
	if err := db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	reopened, err := Open(u.String())
	if err != nil {
		t.Fatalf("Open after baseline: %v", err)
	}
	reopened.Close()

	// PG-1 stores payload as jsonb; the baseline must build on either type.
	t.Run("payload jsonb", func(t *testing.T) {
		s, err := OpenTest()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		db, err := s.DB.DB()
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"run_telemetry", "run_logs"} {
			if _, err := db.Exec("ALTER TABLE " + table + " ALTER COLUMN payload TYPE jsonb USING payload::jsonb"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(file("001_baseline_extensions.sql")); err != nil {
			t.Fatalf("baseline on jsonb payload: %v", err)
		}
		checkGeneratedColumns(t, db)
	})
}

// checkGeneratedColumns asserts the 001 generated columns and their indexes,
// with the same defaults as MySQL migration 001.
func checkGeneratedColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO run_telemetry (run_id, seq, payload) VALUES ('r', 1, '{"source":{"adapter":"otel"}}'), ('r', 2, '{"source":{"adapter":""}}');
	      INSERT INTO run_logs (run_id, seq, payload) VALUES ('r', 1, '{"level":"error"}'), ('r', 2, '{}')`); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM run_telemetry WHERE (seq, adapter) IN ((1, 'otel'), (2, 'unknown'))":                                                         2,
		"SELECT count(*) FROM run_logs WHERE (seq, level) IN ((1, 'error'), (2, 'info'))":                                                                  2,
		"SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname IN ('idx_run_telemetry_run_adapter', 'idx_run_logs_run_level')": 2,
	} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%v: %s", err, query)
		}
		if n != want {
			t.Errorf("%s = %d, want %d", query, n, want)
		}
	}
}
