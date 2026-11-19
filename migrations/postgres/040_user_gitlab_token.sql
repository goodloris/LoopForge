-- PostgreSQL copy of migrations/040_user_gitlab_token.sql for operators who
-- manage DDL by hand. The platform's gorm AutoMigrate already adds this column
-- on startup (see README.md: tables, columns and ordinary indexes are built by
-- gorm), so this is only needed when you provision the schema yourself. It is
-- idempotent and safe to re-run.

ALTER TABLE user_configs ADD COLUMN IF NOT EXISTS gitlab_token TEXT;
