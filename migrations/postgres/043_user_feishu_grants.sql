-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/043_user_feishu_grants.sql: Feishu (飞书) user grants, one row
-- per account that connected Feishu in personal settings. Holds the
-- single-use refresh token and a cached user access token. A grant is not a
-- sign-in identity (see user_identities).
--
-- Apply manually; startup AutoMigrate also creates this table.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/043_user_feishu_grants.sql

CREATE TABLE IF NOT EXISTS user_feishu_grants (
  user_id BIGINT PRIMARY KEY,
  tenant VARCHAR(128) NOT NULL DEFAULT '',
  subject VARCHAR(128) NOT NULL DEFAULT '',
  open_id VARCHAR(128) NOT NULL DEFAULT '',
  display_name VARCHAR(128) NOT NULL DEFAULT '',
  preset VARCHAR(16) NOT NULL DEFAULT '',
  scopes TEXT,
  refresh_token TEXT,
  refresh_expires_at TIMESTAMPTZ,
  access_token TEXT,
  access_expires_at TIMESTAMPTZ,
  status VARCHAR(16) NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ
);
