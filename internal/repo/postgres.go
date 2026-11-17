package repo

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
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
// \u0000, which writes handle explicitly (see nulEscapingCodec).
//
// Migrator is overridden because the driver's own Migrator() embeds the
// inner dialector, whose DataTypeOf would still say json.
type pgDialector struct{ *postgres.Dialector }

// newPostgresDialector opens the pgx pool itself rather than letting the
// driver do it: the driver installs its own AfterConnect hook when the DSN
// names a timezone, and pgx keeps only one, which would drop
// registerPostgresTypes.
func newPostgresDialector(dsn string) (gorm.Dialector, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool := sql.OpenDB(nulEscapingConnector{stdlib.GetConnector(*config, stdlib.OptionAfterConnect(registerPostgresTypes))})
	return pgDialector{&postgres.Dialector{Config: &postgres.Config{DSN: dsn, Conn: pool}}}, nil
}

// registerPostgresTypes adjusts how every pooled connection converts values.
//
// Time: pgx returns timestamptz in time.Local even when the session timezone
// is UTC, so a row serialized to JSON would say +08:00 where MySQL's loc=UTC
// gives Z. Scanning in UTC here fixes every read in one place; the session
// timezone=UTC (openDialector) still governs server-side date arithmetic.
//
// NUL: text and jsonb reject NUL (0x00 in text, \u0000 in jsonb), which
// agent output and logs can contain, and MySQL accepts. []byte parameters
// bound to text or JSON go through nulEscapingCodec here; pgx encodes string
// parameters without consulting any codec, so nulEscapingConn handles those.
// Together no write path can fail on, or drop, the content.
func registerPostgresTypes(_ context.Context, conn *pgx.Conn) error {
	m := conn.TypeMap()
	m.RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
	m.RegisterType(&pgtype.Type{Name: "timestamp", OID: pgtype.TimestampOID, Codec: &pgtype.TimestampCodec{ScanLocation: time.UTC}})
	for _, oid := range []uint32{pgtype.TextOID, pgtype.VarcharOID, pgtype.BPCharOID, pgtype.JSONOID, pgtype.JSONBOID} {
		t, ok := m.TypeForOID(oid)
		if !ok {
			return fmt.Errorf("postgres type %d not registered", oid)
		}
		json := oid == pgtype.JSONOID || oid == pgtype.JSONBOID
		m.RegisterType(&pgtype.Type{Name: t.Name, OID: oid, Codec: nulEscapingCodec{Codec: t.Codec, json: json}})
	}
	return nil
}

// nulEscapingConnector hands out connections whose CheckNamedValue, the
// database/sql hook for converting arguments, escapes NUL in strings. Strings
// carry text, so only a raw 0x00 is rewritten (valid JSON never holds one).
// []byte is left to nulEscapingCodec, which knows whether the parameter is
// bytea and must stay byte-exact.
type nulEscapingConnector struct{ driver.Connector }

func (c nulEscapingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return nulEscapingConn{conn.(*stdlib.Conn)}, nil
}

type nulEscapingConn struct{ *stdlib.Conn }

func (c nulEscapingConn) CheckNamedValue(nv *driver.NamedValue) error {
	if v, ok := nv.Value.(string); ok && strings.IndexByte(v, 0) >= 0 {
		nv.Value = string(escapeNUL([]byte(v), false))
	}
	return c.Conn.CheckNamedValue(nv)
}

// nulEscapingCodec rewrites NUL in encoded parameters as the six visible
// characters \u0000: a raw 0x00 in text, and the JSON escape \u0000 (which
// becomes the escaped text \\u0000) in JSON. Escaping over rejecting: a
// failed write would lose a whole run node or transcript over one byte, and
// over dropping: the marker stays visible where the NUL was. The cost is that
// such values read back as the marker, not the original byte, and cannot be
// told apart from text that literally contained \u0000.
type nulEscapingCodec struct {
	pgtype.Codec
	json bool
}

func (c nulEscapingCodec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	plan := c.Codec.PlanEncode(m, oid, format, value)
	if plan == nil {
		return nil
	}
	return nulEscapingPlan{next: plan, json: c.json}
}

type nulEscapingPlan struct {
	next pgtype.EncodePlan
	json bool
}

func (p nulEscapingPlan) Encode(value any, buf []byte) ([]byte, error) {
	switch v := value.(type) {
	case []byte:
		value = escapeNUL(v, p.json)
	case json.RawMessage:
		value = json.RawMessage(escapeNUL(v, p.json))
	}
	return p.next.Encode(value, buf)
}

// nulEscape is both the JSON escape for NUL and the visible marker for it.
var nulEscape = []byte(`\u0000`)

// escapeNUL returns b with NUL replaced by the visible marker. b is returned
// unchanged (not copied) when it holds no NUL.
func escapeNUL(b []byte, isJSON bool) []byte {
	if !isJSON {
		if bytes.IndexByte(b, 0) < 0 {
			return b
		}
		return bytes.ReplaceAll(b, []byte{0}, nulEscape)
	}
	if !bytes.Contains(b, nulEscape) {
		return b
	}
	// Only a real escape is rewritten: in `\\u0000` the backslash before u
	// is itself escaped, so walk escapes pairwise instead of replacing.
	out := make([]byte, 0, len(b)+8)
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 >= len(b) {
			out = append(out, b[i])
			continue
		}
		if bytes.HasPrefix(b[i:], nulEscape) {
			out = append(out, `\\u0000`...)
			i += len(nulEscape) - 1
			continue
		}
		out = append(out, b[i], b[i+1])
		i++
	}
	return out
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
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_users_login_name_lower ON users (lower(login_name))",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_business_domains_name_lower ON business_domains (lower(name))",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// pgJSONArray is the jsonb array at expr, or an empty array when the value is
// missing or not an array, so jsonb_array_elements/jsonb_array_length never
// fail on legacy rows the way MySQL's JSON_TABLE/JSON_LENGTH tolerate them.
func pgJSONArray(expr string) string {
	return "(CASE WHEN jsonb_typeof(" + expr + ") = 'array' THEN " + expr + " ELSE '[]'::jsonb END)"
}

// sqlColumnText casts a JSON column to text so LIKE/INSTR can search it.
// PostgreSQL needs ::text: CAST(x AS CHAR) there is char(1) and keeps a
// single character, so a LIKE against it silently never matches.
func sqlColumnText(db *gorm.DB, column string) string {
	switch db.Dialector.Name() {
	case "mysql":
		return "CAST(" + column + " AS CHAR)"
	case "postgres":
		return "(" + column + ")::text"
	}
	return "CAST(" + column + " AS TEXT)"
}
