-- Compose preview environments: one isolated `docker compose` project per
-- requirement × repository on the dedicated preview runner
-- (docs/proposals/compose-preview-environments.md). id is the env id
-- pe-<10 hex>; issue_id is empty for runs without a requirement.
-- admin_password is plain text (sandbox previews only).
--
-- Apply manually (idempotent); startup AutoMigrate also creates this table.
-- PostgreSQL: migrations/postgres/046_preview_environments.sql.

CREATE TABLE IF NOT EXISTS preview_environments (
  id VARCHAR(64) NOT NULL PRIMARY KEY,
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
  last_error TEXT NULL,
  claim_nonce VARCHAR(32) NOT NULL DEFAULT '',
  deployed_at DATETIME(3) NULL,
  destroyed_at DATETIME(3) NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  INDEX idx_preview_envs_issue (issue_id),
  INDEX idx_preview_envs_run (last_run_id),
  INDEX idx_preview_envs_status (status)
) DEFAULT CHARSET=utf8mb4;
