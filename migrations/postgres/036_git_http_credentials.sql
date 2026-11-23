-- Idempotent PostgreSQL counterpart of migrations/036_git_http_credentials.sql
-- (upstream #1015/#1232): personal HTTPS Git credentials, one per user and
-- host[:port]. The id is the hex SHA-256 of the host, as the platform
-- computes it, so a host keeps one credential and re-running this script
-- changes nothing.
--
-- Run before starting the version that reads personal Git credentials by
-- host: from then on a plain token in user_configs.git_token is no longer
-- used, so without this backfill users lose GitHub access until they add the
-- token again. The table matches what startup AutoMigrate creates, so the
-- script can run before or after the platform has started once.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/036_git_http_credentials.sql
BEGIN;
CREATE TABLE IF NOT EXISTS user_git_http_credentials (
  user_id bigint NOT NULL,
  id varchar(64) NOT NULL,
  name varchar(80) NOT NULL,
  platform varchar(16) NOT NULL,
  host varchar(255) NOT NULL,
  username varchar(128) NOT NULL,
  token text NOT NULL,
  created_at timestamptz,
  updated_at timestamptz,
  PRIMARY KEY (user_id, id)
);

-- Copy personal access tokens and GitHub device-flow grants to github.com.
-- Managed grants of the deployment adapter (a refresh token or an
-- "oauth2:"/"private:" prefix) stay in user_configs. An existing credential
-- for github.com is kept as it is.
INSERT INTO user_git_http_credentials (user_id, id, name, platform, host, username, token, created_at, updated_at)
SELECT c.user_id, encode(sha256(convert_to('github.com', 'UTF8')), 'hex'), 'GitHub', 'github', 'github.com', 'x-access-token',
       btrim(c.git_token), now(), now()
FROM user_configs c
WHERE COALESCE(btrim(c.git_token), '') <> ''
  AND COALESCE(btrim(c.gongfeng_refresh_token), '') = ''
  AND lower(btrim(c.git_token)) NOT LIKE 'oauth2:%'
  AND lower(btrim(c.git_token)) NOT LIKE 'private:%'
ON CONFLICT (user_id, id) DO NOTHING;

-- The user who registered a delivered MR; background polling prefers that
-- user's credential for the MR host. Startup AutoMigrate also adds it.
ALTER TABLE mr_delivery_states ADD COLUMN IF NOT EXISTS registered_by bigint;
COMMIT;
