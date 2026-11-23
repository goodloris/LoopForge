-- External sign-in identities (Feishu / 飞书 web login today), one row per
-- (provider, tenant, subject). A sign-in matches an account only by this key,
-- never by login name, Chinese name or email. For Feishu, tenant is the
-- tenant_key and subject the union id (or open id when the app has none).
-- A new sign-in provider reuses this table; it adds no users columns.
--
-- Apply manually; startup AutoMigrate also creates this table.
-- PostgreSQL: migrations/postgres/039_user_identities.sql.

CREATE TABLE IF NOT EXISTS user_identities (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  provider VARCHAR(32) NOT NULL,
  tenant VARCHAR(128) NOT NULL DEFAULT '',
  subject VARCHAR(128) NOT NULL,
  display_name VARCHAR(128) NOT NULL DEFAULT '',
  avatar_url TEXT NULL,
  created_at DATETIME(3) NULL,
  last_login_at DATETIME(3) NULL,
  UNIQUE KEY uq_user_identities (provider, tenant, subject),
  KEY idx_user_identities_user (user_id)
) DEFAULT CHARSET=utf8mb4;
