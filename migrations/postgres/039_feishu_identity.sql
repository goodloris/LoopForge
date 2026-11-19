-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/039_feishu_identity.sql: Feishu (飞书) web-login identity columns
-- on users. A sign-in matches a member only by (feishu_tenant_key,
-- feishu_subject), never by login name, Chinese name or email. The columns are
-- NULL for non-Feishu accounts; PostgreSQL's default unique index treats NULLs
-- as distinct, so many such rows coexist.
--
-- Apply manually; startup AutoMigrate also adds these columns and the index.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/039_feishu_identity.sql

ALTER TABLE users ADD COLUMN IF NOT EXISTS feishu_subject VARCHAR(128);
ALTER TABLE users ADD COLUMN IF NOT EXISTS feishu_tenant_key VARCHAR(128);
ALTER TABLE users ADD COLUMN IF NOT EXISTS feishu_avatar_url TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS uq_users_feishu ON users (feishu_tenant_key, feishu_subject);
