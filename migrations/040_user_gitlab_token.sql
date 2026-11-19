-- Personal GitLab personal access token (scope: api) for the single
-- self-hosted GitLab instance configured by LOOPFORGE_GITLAB_URL. Stored
-- separately from the GitHub git_token and never mixed with it. Apply
-- manually; startup does not run this script. gorm AutoMigrate also adds the
-- column on process start (on both MySQL and PostgreSQL), so this script is
-- only for operators who manage DDL by hand. The statement is valid on both
-- MySQL and PostgreSQL; the PostgreSQL copy in migrations/postgres is idempotent.

ALTER TABLE user_configs ADD COLUMN gitlab_token TEXT NULL;
