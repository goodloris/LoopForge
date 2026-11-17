-- PostgreSQL baseline extensions: the schema objects the current code reads
-- that gorm AutoMigrate cannot create. Tables, columns and ordinary indexes
-- come from gorm (repo.Open runs it on every start), so run this after the
-- platform has started once against the database.
--
-- Every statement is IF NOT EXISTS, so the script is safe to repeat.
-- Requires PostgreSQL 12+ (STORED generated columns).

-- 1. Generated columns from MySQL migration 001.
-- Run telemetry filtering by adapter (internal/repo/runs.go adapterSQLFilter),
-- log level filtering and the analytics adapter/level coverage
-- (internal/repo/analytics.go) read these columns instead of parsing every
-- payload. The expressions match MySQL 001: an absent or empty value becomes
-- 'unknown' / 'info'. `->` and `->>` work on json and jsonb alike and are
-- immutable, so the columns survive a json -> jsonb column type in the model.
-- A generated column pins its source column's type: changing payload's type
-- later needs DROP COLUMN adapter/level first, then this script again.
ALTER TABLE run_telemetry
  ADD COLUMN IF NOT EXISTS adapter varchar(255)
    GENERATED ALWAYS AS (COALESCE(NULLIF(payload->'source'->>'adapter', ''), 'unknown')) STORED;
CREATE INDEX IF NOT EXISTS idx_run_telemetry_run_adapter ON run_telemetry (run_id, adapter);

ALTER TABLE run_logs
  ADD COLUMN IF NOT EXISTS level varchar(32)
    GENERATED ALWAYS AS (COALESCE(NULLIF(payload->>'level', ''), 'info')) STORED;
CREATE INDEX IF NOT EXISTS idx_run_logs_run_level ON run_logs (run_id, level);

-- 2. Case-insensitive uniqueness that MySQL gets from utf8mb4_unicode_ci.
-- On MySQL uq_users_login and uq_business_domains_name reject `Alice` next to
-- `alice`; on PostgreSQL the gorm indexes compare case-sensitively. Without
-- these, UsersRepo.UserByLoginName (LOWER(login_name) = LOWER(?) on
-- PostgreSQL) could match two rows and also scans the table.
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_login_lower ON users (lower(login_name));
CREATE UNIQUE INDEX IF NOT EXISTS uq_business_domains_name_lower ON business_domains (lower(name));
