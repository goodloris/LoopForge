// Package repo is the gorm-backed persistence layer of AgentFlow. MySQL and
// PostgreSQL are the production datasources; tests open an in-process sqlite database through
// the same gorm code paths. There is no second in-memory implementation.
package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	ErrRunDecisionStale  = errors.New("run changed before the decision was committed")
	ErrRunLeaseHeld      = errors.New("run lease is held by another instance")
	ErrRunLeaseLost      = errors.New("run lease ownership or generation changed")
	ErrRunWorkspaceBound = errors.New("run workspace is already bound to another host path")
)

// deps 是所有领域 Repo 共享的依赖（repo 层的共享依赖只有 gorm 句柄）。
type deps struct {
	DB *gorm.DB
}

// dialect is gorm's dialector name: "mysql", "postgres" or "sqlite".
func (d *deps) dialect() string {
	if d.DB == nil {
		return ""
	}
	return d.DB.Dialector.Name()
}

func (d *deps) isMySQL() bool { return d.dialect() == "mysql" }

func (d *deps) isPostgres() bool { return d.dialect() == "postgres" }

// dbClockSQL is the database server's own UTC clock at the current statement,
// shared by every replica: UTC_TIMESTAMP(3) on MySQL, statement_timestamp()
// on PostgreSQL (not now(), which is frozen at transaction start). It is ""
// on sqlite, whose single test process has one clock anyway.
func (d *deps) dbClockSQL() string {
	if d.isMySQL() {
		return "UTC_TIMESTAMP(3)"
	}
	if d.isPostgres() {
		return "statement_timestamp()"
	}
	return ""
}

// dbClockBeforeSQL is dbClockSQL minus a bound number of microseconds (one
// "?" argument), or "" on sqlite.
func (d *deps) dbClockBeforeSQL() string {
	if d.isMySQL() {
		return "UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND"
	}
	if d.isPostgres() {
		return "statement_timestamp() - ? * interval '1 microsecond'"
	}
	return ""
}

// likeCI is "(column LIKE ?)" matched without regard to case on every
// dialect, the way MySQL's utf8mb4_unicode_ci and sqlite's LIKE already
// compare. PostgreSQL's LIKE is case-sensitive, so it uses ILIKE. Use it for
// free text people name or write (artifact names, content); IDs, tokens and
// paths keep a plain LIKE, case-sensitive on PostgreSQL. column must be a
// code constant.
func (d *deps) likeCI(column string) string {
	if d.isPostgres() {
		return "(" + column + " ILIKE ?)"
	}
	return "(" + column + " LIKE ?)"
}

// pgJSONText extracts text at a MySQL-style JSON path ("$.a.b[0]") from a
// jsonb column on PostgreSQL. The jsonpath syntax accepts the same paths the
// MySQL branches use, so callers keep one path constant. arg is either a
// bound "?" or a quoted literal.
func pgJSONText(column, arg string) string {
	return "(jsonb_path_query_first(" + column + ", " + arg + "::jsonpath) #>> '{}')"
}

// sqlJSONText returns a portable "extract text at JSON path" expression.
// MySQL needs the JSON_UNQUOTE wrapper; sqlite's json_extract already returns
// unquoted text for string values.
func (d *deps) sqlJSONText(path string) string {
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(payload, ?))"
	}
	if d.isPostgres() {
		return pgJSONText("payload", "?")
	}
	return "json_extract(payload, ?)"
}

// sqlPayloadJSONText is the literal-path variant of sqlJSONText for SELECT
// projections that cannot bind the path as a parameter. path is the JSON path
// below "$." and must be a code constant, never user input.
func (d *deps) sqlPayloadJSONText(path string) string {
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(payload, '$." + path + "'))"
	}
	if d.isPostgres() {
		return pgJSONText("payload", "'$."+path+"'")
	}
	return "json_extract(payload, '$." + path + "')"
}

// sqlExecutionContextJSONText is the execution_context-column counterpart of
// sqlPayloadJSONText. path is the JSON path below "$." and must be a code
// constant, never user input.
func (d *deps) sqlExecutionContextJSONText(path string) string {
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(execution_context, '$." + path + "'))"
	}
	if d.isPostgres() {
		return pgJSONText("execution_context", "'$."+path+"'")
	}
	return "json_extract(execution_context, '$." + path + "')"
}

// locking returns the query with a row-lock clause on MySQL and PostgreSQL.
// sqlite serializes writers already (single connection in tests), so no
// clause is needed there.
func (d *deps) locking(q *gorm.DB) *gorm.DB {
	if d.isMySQL() || d.isPostgres() {
		return q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}

// CatalogRepo owns the reusable definitions: agents, rules, skills, MCP
// servers, executors, dockerfiles, environments, sandboxes, workflows,
// repositories, sync config, schedule jobs and definition revisions.
type CatalogRepo struct {
	*deps
	projects      *ProjectsRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// ProjectsRepo owns projects and their members.
type BusinessDomainsRepo struct {
	*deps
}

type ProjectsRepo struct {
	*deps
}

// IssuesRepo owns issues and issue comments.
type IssuesRepo struct {
	*deps
	mu       sync.Mutex
	projects *ProjectsRepo
	users    *UsersRepo
	tasks    *TasksRepo
}

// TasksRepo owns tasks (the cross-instance idempotency records).
type TasksRepo struct {
	*deps
}

// UsersRepo owns users and their saved credentials.
type UsersRepo struct {
	*deps
}

// TokensRepo owns API tokens.
type TokensRepo struct {
	*deps
}

// AssistantRepo owns Studio AI assistant conversations and scheduled prompts.
type AssistantRepo struct {
	*deps
	users         *UsersRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// RunsRepo owns runs, their children (nodes/events/telemetry/logs/artifacts),
// run leases and workflow metrics.
type RunsRepo struct {
	*deps
	assistant     *AssistantRepo
	issues        *IssuesRepo
	catalog       *CatalogRepo
	notifications *NotificationsRepo
	*BotInteractionsRepo
}

// RunnersRepo owns the CLI runner registry, runner tasks and runner events.
type RunnersRepo struct {
	*deps
}

// DeliveryRepo owns MR delivery state and the delivery locking that follows.
type DeliveryRepo struct {
	*deps
	issues  *IssuesRepo
	tasks   *TasksRepo
	catalog *CatalogRepo
	runs    *RunsRepo
}

// AnalyticsRepo owns the read-only operational analytics view.
type AnalyticsRepo struct {
	*deps
	runs     *RunsRepo
	catalog  *CatalogRepo
	delivery *DeliveryRepo
	issues   *IssuesRepo
	tasks    *TasksRepo
	projects *ProjectsRepo
	insights *InsightsRepo
}

// Store 组合各领域 Repo。领域 Repo 以匿名指针嵌入（未导出类型）：包外仅能
// 通过提升的方法调用，与现有调用形态完全兼容（方法提升对未导出类型同样生效）。
type Store struct {
	*deps
	*CatalogRepo
	*ProjectsRepo
	*BusinessDomainsRepo
	*IssuesRepo
	*TasksRepo
	*UsersRepo
	*TokensRepo
	*AssistantRepo
	*RunsRepo
	*RunnersRepo
	*DeliveryRepo
	*AnalyticsRepo
	*InsightsRepo
	*PreviewsRepo
	*EngineInstancesRepo
	*RoomsRepo
	*SpacesRepo
	*KnowledgeRepo
	*SSHHostTrustRepo
	*HTTPSHostTrustRepo
	*NotificationsRepo
	*BotInteractionsRepo
}

// newStore wires the shared deps and every cross-domain pointer. Domains have
// no cycles, so a single construction pass is enough.
func newStore(db *gorm.DB) *Store {
	d := &deps{DB: db}
	s := &Store{deps: d}
	s.ProjectsRepo = &ProjectsRepo{deps: d}
	s.BusinessDomainsRepo = &BusinessDomainsRepo{deps: d}
	s.UsersRepo = &UsersRepo{deps: d}
	s.BotInteractionsRepo = &BotInteractionsRepo{deps: d}
	s.TokensRepo = &TokensRepo{deps: d}
	s.AssistantRepo = &AssistantRepo{deps: d, users: s.UsersRepo}
	s.TasksRepo = &TasksRepo{deps: d}
	s.NotificationsRepo = &NotificationsRepo{deps: d, users: s.UsersRepo, projects: s.ProjectsRepo, interactions: s.BotInteractionsRepo}
	s.AssistantRepo.notifications = s.NotificationsRepo
	s.CatalogRepo = &CatalogRepo{deps: d, projects: s.ProjectsRepo, notifications: s.NotificationsRepo}
	s.IssuesRepo = &IssuesRepo{deps: d, projects: s.ProjectsRepo, users: s.UsersRepo, tasks: s.TasksRepo}
	s.NotificationsRepo.issues = s.IssuesRepo
	s.RunsRepo = &RunsRepo{deps: d, assistant: s.AssistantRepo, issues: s.IssuesRepo, catalog: s.CatalogRepo, notifications: s.NotificationsRepo}
	s.RunnersRepo = &RunnersRepo{deps: d}
	s.DeliveryRepo = &DeliveryRepo{deps: d, issues: s.IssuesRepo, tasks: s.TasksRepo, catalog: s.CatalogRepo, runs: s.RunsRepo}
	s.InsightsRepo = &InsightsRepo{deps: d}
	s.AnalyticsRepo = &AnalyticsRepo{deps: d, runs: s.RunsRepo, catalog: s.CatalogRepo, delivery: s.DeliveryRepo, issues: s.IssuesRepo, tasks: s.TasksRepo, projects: s.ProjectsRepo, insights: s.InsightsRepo}
	s.PreviewsRepo = &PreviewsRepo{deps: d}
	s.EngineInstancesRepo = &EngineInstancesRepo{deps: d}
	s.RoomsRepo = &RoomsRepo{deps: d}
	s.SpacesRepo = &SpacesRepo{deps: d}
	s.KnowledgeRepo = &KnowledgeRepo{deps: d}
	s.SSHHostTrustRepo = &SSHHostTrustRepo{deps: d}
	s.HTTPSHostTrustRepo = &HTTPSHostTrustRepo{deps: d}
	return s
}

// Open connects the store to its database, MySQL or PostgreSQL chosen by the
// DSN: "postgres://" / "postgresql://" URLs select PostgreSQL, anything else
// is a go-sql-driver/mysql DSN. An empty DSN falls back to
// AGENTFLOW_DATABASE_DSN, then the legacy AGENTFLOW_MYSQL_DSN; with neither
// set it is a configuration error, not a reason to invent state elsewhere.
func Open(dsn string) (*Store, error) {
	dsn = resolveDSN(dsn)
	if dsn == "" {
		return nil, errors.New("AGENTFLOW_DATABASE_DSN（或 AGENTFLOW_MYSQL_DSN）未配置：缺少数据库 DSN 时拒绝启动")
	}
	dialector, name, err := openDialector(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s dsn: %w", name, err)
	}
	db, err := gorm.Open(dialector, &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("%s open: %w", name, err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(32)
		sqlDB.SetMaxIdleConns(8)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
		if err := sqlDB.Ping(); err != nil {
			return nil, fmt.Errorf("%s ping: %w", name, err)
		}
	}
	s := newStore(db)
	// Incremental DDL only (missing columns/tables/indexes) via gorm's
	// migrator. Schema baselines, data migrations and seed data are NOT run
	// from code: they are plain SQL scripts under migrations/, applied
	// manually by the operator.
	if err := autoMigrateIncremental(s.DB); err != nil {
		s.Close()
		return nil, fmt.Errorf("auto migrate: %w", err)
	}
	return s, nil
}

// resolveDSN returns the explicit DSN, else AGENTFLOW_DATABASE_DSN, else the
// legacy AGENTFLOW_MYSQL_DSN that existing deployments still set.
func resolveDSN(dsn string) string {
	for _, v := range []string{dsn, os.Getenv("AGENTFLOW_DATABASE_DSN"), os.Getenv("AGENTFLOW_MYSQL_DSN")} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// isPostgresDSN reports a libpq URL DSN. Key/value libpq DSNs ("host=...")
// are not detected; they would be mistaken for MySQL DSNs.
func isPostgresDSN(dsn string) bool {
	lower := strings.ToLower(dsn)
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

// openDialector picks the gorm dialector for dsn. MySQL DSNs get the utf8mb4
// charset and UTC time parsing the schema relies on; PostgreSQL connections
// are pinned to UTC so timestamp text round-trips like MySQL's loc=UTC.
func openDialector(dsn string) (gorm.Dialector, string, error) {
	if isPostgresDSN(dsn) {
		if !strings.Contains(dsn, "timezone=") {
			if strings.Contains(dsn, "?") {
				dsn += "&timezone=UTC"
			} else {
				dsn += "?timezone=UTC"
			}
		}
		d, err := newPostgresDialector(dsn)
		return d, "postgres", err
	}
	if !strings.Contains(dsn, "charset=") {
		if strings.Contains(dsn, "?") {
			dsn += "&charset=utf8mb4&collation=utf8mb4_unicode_ci"
		} else {
			dsn += "?charset=utf8mb4&collation=utf8mb4_unicode_ci"
		}
	}
	if !strings.Contains(dsn, "parseTime=") {
		dsn += "&parseTime=true&loc=UTC"
	}
	return mysql.Open(dsn), "mysql", nil
}

// Close releases the underlying connection pool. The gorm handle is kept so
// in-flight background goroutines fail gracefully ("sql: database is closed")
// instead of panicking on a nil handle.
func (s *Store) Close() {
	if s.DB == nil {
		return
	}
	if sqlDB, err := s.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

func (s *Store) StorageInfo() map[string]any {
	return map[string]any{
		"driver":   s.DB.Dialector.Name(),
		"database": true,
		"note":     "所有状态都在 MySQL：项目、Issue、SOP、运行记录、助手对话和定时任务都直接读写数据库。",
	}
}

func IssueBoundToTracker(issue domain.Issue) bool {
	if issue.Source != "gongfeng" && issue.Source != "tapd" {
		return false
	}
	if issue.ExternalRef == nil {
		return false
	}
	return strings.TrimSpace(issue.ExternalRef.ExternalID) != "" || issue.ExternalRef.ExternalIID != 0
}

// RestoreTrackerBinding fills Source/ExternalRef from a still-bound snapshot, or
// from a stable local id (tapd-… / gf-…) after a save wiped the pointer.
func RestoreTrackerBinding(issue *domain.Issue, existing domain.Issue) {
	if issue == nil || IssueBoundToTracker(*issue) {
		return
	}
	if IssueBoundToTracker(existing) {
		issue.Source = existing.Source
		issue.ExternalRef = existing.ExternalRef
		if strings.TrimSpace(issue.ID) == "" {
			issue.ID = existing.ID
		}
		return
	}
	id := strings.TrimSpace(issue.ID)
	if id == "" {
		id = strings.TrimSpace(existing.ID)
		issue.ID = id
	}
	if extID, ok := strings.CutPrefix(id, "tapd-"); ok && strings.TrimSpace(extID) != "" {
		issue.Source = "tapd"
		ref := domain.ExternalRef{Source: "tapd", ExternalID: extID, ExternalType: "story"}
		if existing.ExternalRef != nil {
			ref.ExternalURL = existing.ExternalRef.ExternalURL
			ref.ExternalState = existing.ExternalRef.ExternalState
		}
		issue.ExternalRef = &ref
		return
	}
	if _, iid := parseStoredGongfengIssueID(id); iid > 0 {
		issue.Source = "gongfeng"
		if existing.ExternalRef != nil {
			issue.ExternalRef = existing.ExternalRef
			return
		}
		issue.ExternalRef = &domain.ExternalRef{Source: "gongfeng", ExternalIID: iid, ExternalType: "issue"}
	}
}

func parseStoredGongfengIssueID(id string) (repo string, iid int) {
	body := strings.TrimPrefix(strings.TrimSpace(id), "gf-")
	if body == "" || body == strings.TrimSpace(id) {
		return "", 0
	}
	i := strings.LastIndex(body, "-")
	if i <= 0 {
		return "", 0
	}
	n := 0
	for _, ch := range body[i+1:] {
		if ch < '0' || ch > '9' {
			return "", 0
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return "", 0
	}
	return body[:i], n
}

func cloneRunNodes(nodes map[string]*domain.NodeRun) map[string]*domain.NodeRun {
	if len(nodes) == 0 {
		return nil
	}
	out := make(map[string]*domain.NodeRun, len(nodes))
	for id, node := range nodes {
		if node == nil {
			continue
		}
		cp := *node
		out[id] = &cp
	}
	return out
}

func cloneRun(run domain.Run) domain.Run {
	run.Nodes = cloneRunNodes(run.Nodes)
	run.Events = append([]domain.RunEvent(nil), run.Events...)
	run.Telemetry = append([]domain.TelemetryEvent(nil), run.Telemetry...)
	run.Logs = append([]domain.LogRecord(nil), run.Logs...)
	run.Artifacts = append([]domain.Artifact(nil), run.Artifacts...)
	return run
}

func normalizeRefs(values []string, prefix string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimPrefix(v, prefix)
		v = strings.TrimSuffix(v, ".md")
		out = append(out, v)
	}
	return out
}

func sortSlice[T any](values []T, less func(a, b T) bool) {
	sort.Slice(values, func(i, j int) bool { return less(values[i], values[j]) })
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}
