-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/044_loopool_builtin_labels.sql: the welcome project and the
-- built-in SOP author tag say LooPool instead of LoopForge or Bagel. See the
-- MySQL script for what changes.
--
-- New installs get these labels from migrations/postgres/seed. This script is
-- not run on install or ./start.sh pull; an operator runs it by hand (042 does
-- not need to run first):
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/044_loopool_builtin_labels.sql
-- Only the shipped values change: a renamed welcome project and an edited
-- owner are left alone.
SET client_encoding = 'UTF8';
BEGIN;
UPDATE projects SET name = 'LooPool 体验项目', updated_at = now()
WHERE id = 'welcome' AND name IN ('LoopForge 体验项目', 'Bagel 体验项目');
UPDATE projects SET payload = jsonb_set(payload::jsonb, '{name}', '"LooPool 体验项目"'), updated_at = now()
WHERE id = 'welcome' AND payload::jsonb ->> 'name' IN ('LoopForge 体验项目', 'Bagel 体验项目');
UPDATE generic_templates SET payload = jsonb_set(payload::jsonb, '{Workflow,owner}', '"LooPool"'), updated_at = now()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND payload::jsonb #>> '{Workflow,owner}' IN ('LoopForge', 'Bagel');
COMMIT;
