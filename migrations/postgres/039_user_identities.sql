-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/039_user_identities.sql: external sign-in identities (Feishu /
-- 飞书 web login today), one row per (provider, tenant, subject). A sign-in
-- matches an account only by this key, never by login name, Chinese name or
-- email.
--
-- Apply manually; startup AutoMigrate also creates this table.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/039_user_identities.sql

CREATE TABLE IF NOT EXISTS user_identities (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  provider VARCHAR(32) NOT NULL,
  tenant VARCHAR(128) NOT NULL DEFAULT '',
  subject VARCHAR(128) NOT NULL,
  display_name VARCHAR(128) NOT NULL DEFAULT '',
  avatar_url TEXT,
  created_at TIMESTAMPTZ,
  last_login_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_user_identities ON user_identities (provider, tenant, subject);
CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities (user_id);
