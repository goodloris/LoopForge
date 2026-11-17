package repo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// payloadRow is the generic projection used to read JSON-payload tables.
type payloadRow struct {
	ID      string     `gorm:"column:id"`
	Name    string     `gorm:"column:name"`
	Payload []byte     `gorm:"column:payload"`
	Created *time.Time `gorm:"column:created_at"`
	Updated *time.Time `gorm:"column:updated_at"`
}

// payloadOne reads a single JSON payload row by primary key.
func payloadOne[T any](db *gorm.DB, table, id string) (T, bool) {
	var zero T
	var row payloadRow
	if err := db.Table(table).Where("id = ?", id).Take(&row).Error; err != nil {
		return zero, false
	}
	var item T
	if json.Unmarshal(row.Payload, &item) != nil {
		return zero, false
	}
	return item, true
}

// payloadList reads every JSON payload row matching the optional condition.
func payloadList[T any](db *gorm.DB, table, query string, args ...any) []T {
	q := db.Table(table)
	if query != "" {
		q = q.Where(query, args...)
	}
	var rows []payloadRow
	if q.Find(&rows).Error != nil {
		return []T{}
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		var item T
		if json.Unmarshal(row.Payload, &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

// upsert inserts a row, replacing every column when the primary key already
// exists. Callers pass the full desired row; CreatedAt preservation is the
// caller's responsibility (it copies the old value before writing).
func upsert(db *gorm.DB, table string, values map[string]any) error {
	cols := make([]string, 0, len(values))
	for name := range values {
		cols = append(cols, name)
	}
	sort.Strings(cols)
	cl := clause.OnConflict{Columns: conflictTarget(db, table), DoUpdates: clause.AssignmentColumns(cols)}
	if err := db.Table(table).Clauses(cl).Create(values).Error; err != nil {
		return fmt.Errorf("%s write: %w", table, err)
	}
	return nil
}

// primaryKeysByTable caches each model table's primary key columns.
var primaryKeysByTable sync.Map

// conflictTarget names the primary key as the ON CONFLICT target on
// PostgreSQL, which (unlike MySQL's ON DUPLICATE KEY and sqlite) rejects
// DO UPDATE without one. Other dialects keep their implicit any-unique-key
// target, so their behaviour is unchanged.
func conflictTarget(db *gorm.DB, table string) []clause.Column {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if cached, ok := primaryKeysByTable.Load(table); ok {
		return cached.([]clause.Column)
	}
	var cols []clause.Column
	for _, model := range allModels() {
		stmt := &gorm.Statement{DB: db}
		if stmt.Parse(model) != nil || stmt.Schema.Table != table {
			continue
		}
		for _, name := range stmt.Schema.PrimaryFieldDBNames {
			cols = append(cols, clause.Column{Name: name})
		}
		break
	}
	primaryKeysByTable.Store(table, cols)
	return cols
}

// upsertColumns inserts a row and updates only the given columns on conflict.
func upsertColumns(db *gorm.DB, table string, values map[string]any, updateCols []string) error {
	cl := clause.OnConflict{Columns: conflictTarget(db, table), DoUpdates: clause.AssignmentColumns(updateCols)}
	if err := db.Table(table).Clauses(cl).Create(values).Error; err != nil {
		return fmt.Errorf("%s write: %w", table, err)
	}
	return nil
}

// insertIgnore inserts a row, keeping the existing row on conflict. gorm's
// OnConflict{DoNothing} renders an empty ON DUPLICATE KEY UPDATE clause on
// MySQL, so the dialect-specific statement is built here. Table and column
// names are compile-time literals; values are always bound as parameters.
func insertIgnore(db *gorm.DB, table string, values map[string]any) (bool, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	cols := make([]string, len(names))
	placeholders := make([]string, len(names))
	args := make([]any, 0, len(names))
	for i, name := range names {
		cols[i] = "`" + name + "`"
		placeholders[i] = "?"
		args = append(args, values[name])
	}
	verb, tail := "INSERT OR IGNORE", ""
	switch db.Dialector.Name() {
	case "mysql":
		verb = "INSERT IGNORE"
	case "postgres":
		verb, tail = "INSERT", " ON CONFLICT DO NOTHING"
		for i, name := range names {
			cols[i] = `"` + name + `"`
		}
	}
	stmt := fmt.Sprintf("%s INTO %s (%s) VALUES (%s)%s", verb, table, strings.Join(cols, ","), strings.Join(placeholders, ","), tail)
	res := db.Exec(stmt, args...)
	return res.RowsAffected > 0, res.Error
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t.UTC()
	return &value
}

func jsonBytes(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
