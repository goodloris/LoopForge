package repo

import (
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// pgDialector is the PostgreSQL dialector with one change: columns tagged
// `type:json` are created as jsonb. The tag stays the same for every dialect
// (json on MySQL and sqlite), so models need no per-dialect types and new
// `type:json` fields follow automatically.
//
// jsonb over json: queries use jsonb operators without a per-row cast, and
// the columns can carry GIN or expression indexes. jsonb does not keep key
// order or duplicate keys (nothing here relies on either) and rejects
// \u0000, which writes handle explicitly (see pgSafeJSON).
//
// Migrator is overridden because the driver's own Migrator() embeds the
// inner dialector, whose DataTypeOf would still say json.
type pgDialector struct{ *postgres.Dialector }

func newPostgresDialector(dsn string) gorm.Dialector {
	return pgDialector{postgres.Open(dsn).(*postgres.Dialector)}
}

func (d pgDialector) DataTypeOf(field *schema.Field) string {
	if strings.EqualFold(string(field.DataType), "json") {
		return "jsonb"
	}
	return d.Dialector.DataTypeOf(field)
}

func (d pgDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
		DB:                          db,
		Dialector:                   d,
		CreateIndexAfterCreateTable: true,
	}}}
}
