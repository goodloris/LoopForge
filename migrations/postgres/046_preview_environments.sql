-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/046_preview_environments.sql: compose preview environments, one
-- per requirement × repository (docs/proposals/compose-preview-environments.md).
-- admin_password is plain text (sandbox previews only).
--
-- Apply manually; startup AutoMigrate also creates this table.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/046_preview_environments.sql

CREATE TABLE IF NOT EXISTS preview_environments (
  id VARCHAR(64) PRIMARY KEY,
  project_id VARCHAR(64) NOT NULL DEFAULT '',
  issue_id VARCHAR(64) NOT NULL DEFAULT '',
  repository_id VARCHAR(64) NOT NULL DEFAULT '',
  runner_id VARCHAR(64) NOT NULL DEFAULT '',
  last_run_id VARCHAR(64) NOT NULL DEFAULT '',
  branch VARCHAR(255) NOT NULL DEFAULT '',
  sha VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT '',
  url VARCHAR(1024) NOT NULL DEFAULT '',
  admin_password VARCHAR(128) NOT NULL DEFAULT '',
  last_error TEXT,
  claim_nonce VARCHAR(32) NOT NULL DEFAULT '',
  deployed_at TIMESTAMPTZ,
  destroyed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_preview_envs_issue ON preview_environments (issue_id);
CREATE INDEX IF NOT EXISTS idx_preview_envs_run ON preview_environments (last_run_id);
CREATE INDEX IF NOT EXISTS idx_preview_envs_status ON preview_environments (status);
