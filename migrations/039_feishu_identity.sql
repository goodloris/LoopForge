-- Feishu (飞书) web-login identity columns on users. A sign-in matches a
-- member only by (feishu_tenant_key, feishu_subject), where feishu_subject is
-- the union id (or open id when the app has no union id); it never matches an
-- existing account by login name, Chinese name or email. The columns are NULL
-- for accounts that never signed in through Feishu, and the unique index
-- tolerates many such rows because SQL treats NULLs as distinct.
--
-- Apply manually; startup AutoMigrate also adds these columns and the index.
-- PostgreSQL: migrations/postgres/039_feishu_identity.sql.

ALTER TABLE users ADD COLUMN feishu_subject VARCHAR(128) NULL;
ALTER TABLE users ADD COLUMN feishu_tenant_key VARCHAR(128) NULL;
ALTER TABLE users ADD COLUMN feishu_avatar_url TEXT NULL;
CREATE UNIQUE INDEX uq_users_feishu ON users (feishu_tenant_key, feishu_subject);
