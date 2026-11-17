package repo

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// sqliteDeps and postgresDeps build dialect handles directly, independent of
// the database OpenTest is backed by, so SQL text is asserted per dialect.
func sqliteDeps(t *testing.T) *deps {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return &deps{DB: db}
}

func postgresDeps(t *testing.T) *deps {
	t.Helper()
	db, err := gorm.Open(newPostgresDialector("postgres://u:p@127.0.0.1:1/x"), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return &deps{DB: db}
}

func TestSQLPayloadJSONTextSQLite(t *testing.T) {
	d := sqliteDeps(t)
	cases := map[string]string{
		"tapdId":                  "json_extract(payload, '$.tapdId')",
		"externalRef.externalUrl": "json_extract(payload, '$.externalRef.externalUrl')",
		"clarification.startedAt": "json_extract(payload, '$.clarification.startedAt')",
	}
	for path, want := range cases {
		if got := d.sqlPayloadJSONText(path); got != want {
			t.Fatalf("sqlPayloadJSONText(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSQLPayloadJSONTextPostgres(t *testing.T) {
	d := postgresDeps(t)
	want := "(jsonb_path_query_first(payload, '$.externalRef.externalUrl'::jsonpath) #>> '{}')"
	if got := d.sqlPayloadJSONText("externalRef.externalUrl"); got != want {
		t.Fatalf("sqlPayloadJSONText = %q, want %q", got, want)
	}
}

func TestSQLPayloadJSONTextMySQL(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(127.0.0.1:1)/agentflow",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	d := &deps{DB: db}
	if !d.isMySQL() {
		t.Fatal("expected mysql dialect")
	}
	cases := map[string]string{
		"deliveryState":           "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deliveryState'))",
		"externalRef.externalUrl": "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.externalRef.externalUrl'))",
		"operations":              "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.operations'))",
	}
	for path, want := range cases {
		if got := d.sqlPayloadJSONText(path); got != want {
			t.Fatalf("sqlPayloadJSONText(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSQLExecutionContextJSONText(t *testing.T) {
	lite := sqliteDeps(t)
	pg := postgresDeps(t)
	mysqlDB, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(127.0.0.1:1)/agentflow",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	my := &deps{DB: mysqlDB}
	cases := []struct {
		d    *deps
		path string
		want string
	}{
		{lite, "issueId", "json_extract(execution_context, '$.issueId')"},
		{lite, "projectId", "json_extract(execution_context, '$.projectId')"},
		{pg, "issueId", "(jsonb_path_query_first(execution_context, '$.issueId'::jsonpath) #>> '{}')"},
		{my, "issueId", "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.issueId'))"},
		{my, "projectId", "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$.projectId'))"},
	}
	for _, c := range cases {
		if got := c.d.sqlExecutionContextJSONText(c.path); got != c.want {
			t.Fatalf("sqlExecutionContextJSONText(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
