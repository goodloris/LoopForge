-- Idempotent PostgreSQL counterpart of migrations/037_retire_plain_git_token.sql
-- (upstream). Run after 036_git_http_credentials.sql, after checking its
-- backfill (every user with a plain token has a github.com row with the same
-- token) and leaving time for a rollback: this clears user_configs.git_token.
-- Only verified copies are cleared: a token without an identical github.com
-- credential, and managed grants (refresh token, "oauth2:"/"private:"), stay.
-- PostgreSQL compares text byte-for-byte, so tokens differing only in case
-- never match.
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/037_retire_plain_git_token.sql
BEGIN;
UPDATE user_configs c
SET git_token = NULL
FROM user_git_http_credentials h
WHERE h.user_id = c.user_id
  AND h.id = encode(sha256(convert_to('github.com', 'UTF8')), 'hex')
  AND h.host = 'github.com'
  AND COALESCE(btrim(c.git_token), '') <> ''
  AND COALESCE(btrim(c.gongfeng_refresh_token), '') = ''
  AND lower(btrim(c.git_token)) NOT LIKE 'oauth2:%'
  AND lower(btrim(c.git_token)) NOT LIKE 'private:%'
  AND h.token = btrim(c.git_token);

-- All configured credentials are available to the owner's workspace; remove
-- the former exposure toggle where an early 036 created it.
ALTER TABLE user_git_http_credentials DROP COLUMN IF EXISTS expose_to_workspace;
COMMIT;
