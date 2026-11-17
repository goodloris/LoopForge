package repo

import (
	"fmt"
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

// postgresCaseInsensitiveIndexes adds the unique lower() indexes that make
// login names and business domain names case-insensitive on PostgreSQL, as
// utf8mb4_unicode_ci already makes them on MySQL. gorm index tags cannot
// express them per dialect (MySQL would need ((lower(col))) and a different
// schema than existing installs), so this is the one piece of incremental DDL
// Open adds by hand. It never rewrites data: duplicate names differing only in
// case make the CREATE fail and Open refuse to start, which an operator must
// resolve.
func postgresCaseInsensitiveIndexes(db *gorm.DB) error {
	for _, stmt := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_users_login_ci ON users (lower(login_name))",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_business_domains_name_ci ON business_domains (lower(name))",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}
