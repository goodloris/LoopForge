-- Feishu (飞书) user grants: one row per account that connected Feishu in
-- personal settings, so agents in conversation workspaces can call lark-cli
-- as the member who sent the round. Holds the single-use refresh token and
-- a cached user access token; neither leaves the platform except the access
-- token injected into a round. A grant is not a sign-in identity (see
-- user_identities).
--
-- Apply manually (idempotent); startup AutoMigrate also creates this table.
-- PostgreSQL: migrations/postgres/043_user_feishu_grants.sql.

CREATE TABLE IF NOT EXISTS user_feishu_grants (
  user_id BIGINT NOT NULL PRIMARY KEY,
  tenant VARCHAR(128) NOT NULL DEFAULT '',
  subject VARCHAR(128) NOT NULL DEFAULT '',
  open_id VARCHAR(128) NOT NULL DEFAULT '',
  display_name VARCHAR(128) NOT NULL DEFAULT '',
  preset VARCHAR(16) NOT NULL DEFAULT '',
  scopes TEXT NULL,
  refresh_token TEXT NULL,
  refresh_expires_at DATETIME(3) NULL,
  access_token TEXT NULL,
  access_expires_at DATETIME(3) NULL,
  status VARCHAR(16) NOT NULL DEFAULT '',
  updated_at DATETIME(3) NULL
) DEFAULT CHARSET=utf8mb4;
