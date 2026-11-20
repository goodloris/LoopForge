-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/041_builtin_roles_default_executor.sql: built-in roles follow the
-- deployment's default executor instead of naming CodeBuddy. See the MySQL
-- script for what changes.
--
-- New installs get these definitions from migrations/postgres/seed. This
-- script changes the executor of existing built-in roles, so it is not run on
-- install or ./start.sh pull; an operator runs it by hand:
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/041_builtin_roles_default_executor.sql
-- Only executorId "codebuddy" is removed; a role switched to another CLI keeps
-- it. Project SOPs and roles already copied from these templates are copies
-- and are not touched.
BEGIN;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents}', (
      SELECT jsonb_agg(CASE WHEN a ->> 'executorId' = 'codebuddy' THEN a - 'executorId' ELSE a END ORDER BY i)
      FROM jsonb_array_elements(payload::jsonb -> 'Agents') WITH ORDINALITY AS t(a, i))),
    updated_at = now()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND payload::jsonb -> 'Agents' @> '[{"executorId": "codebuddy"}]'::jsonb;
UPDATE agents
SET payload = payload::jsonb - 'executorId', updated_at = now()
WHERE id = 'baseline-align-resolver'
  AND payload::jsonb ->> 'executorId' = 'codebuddy';
COMMIT;
