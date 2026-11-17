package repo

import (
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The blob columns carry a size instead of a MySQL-only type so one tag maps
// to mediumblob/longblob on MySQL and bytea on PostgreSQL. No connection is
// opened: both drivers open lazily and DataTypeOf is pure.
func TestBlobColumnTypesPerDialect(t *testing.T) {
	my, err := gorm.Open(mysql.New(mysql.Config{DSN: "u:p@tcp(127.0.0.1:1)/x", SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	pg, err := gorm.Open(postgres.New(postgres.Config{DSN: "postgres://u:p@127.0.0.1:1/x"}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model             any
		column            string
		mysqlType, pgType string
	}{
		{skillFileRow{}, "content", "mediumblob", "bytea"},
		{mcpFileRow{}, "content", "mediumblob", "bytea"},
		{workspaceCheckpointChunk{}, "data", "mediumblob", "bytea"},
		{WorkspaceCheckpoint{}, "archive", "mediumblob", "bytea"},
		{previewBlobRow{}, "content", "longblob", "bytea"},
		{pageFeedbackImageRow{}, "data", "longblob", "bytea"},
	} {
		for _, d := range []struct {
			db   *gorm.DB
			want string
		}{{my, tc.mysqlType}, {pg, tc.pgType}} {
			stmt := &gorm.Statement{DB: d.db}
			if err := stmt.Parse(tc.model); err != nil {
				t.Fatal(err)
			}
			field := stmt.Schema.LookUpField(tc.column)
			if field == nil {
				t.Fatalf("%T has no column %s", tc.model, tc.column)
			}
			if got := d.db.Dialector.DataTypeOf(field); got != d.want {
				t.Errorf("%s %T.%s = %s, want %s", d.db.Dialector.Name(), tc.model, tc.column, got, d.want)
			}
		}
	}
}
