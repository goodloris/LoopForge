-- Personal GitLab personal access token (scope: api) for the single
-- self-hosted GitLab instance configured by LOOPFORGE_GITLAB_URL. Stored
-- separately from the GitHub git_token and never mixed with it. Apply
-- manually; startup does not run this script. gorm AutoMigrate also adds the
-- column on process start.

ALTER TABLE user_configs ADD COLUMN gitlab_token TEXT NULL;
