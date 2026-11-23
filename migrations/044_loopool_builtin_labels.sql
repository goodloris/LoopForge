-- Idempotent MySQL migration: the built-in labels a user reads say LooPool.
--
-- The welcome project was seeded as 「LoopForge 体验项目」 (renamed to
-- 「Bagel 体验项目」 by 042) and the four built-in SOP templates carry
-- Workflow.owner "LoopForge" or "Bagel", which the SOP cards show as their
-- author tag. New installs get the LooPool labels from the seed. This script
-- is not run on install or ./start.sh pull; an operator runs it by hand (042
-- does not need to run first):
--   docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/044_loopool_builtin_labels.sql
-- Only the shipped values change: a renamed welcome project and an edited
-- owner are left alone. Project SOPs already copied from these templates are
-- copies and are not touched. Internal identifiers (project key LOOP and so
-- on) are unchanged.
SET NAMES utf8mb4;
UPDATE projects SET name = 'LooPool 体验项目', updated_at = UTC_TIMESTAMP()
WHERE id = 'welcome' AND name IN ('LoopForge 体验项目', 'Bagel 体验项目');
UPDATE projects SET payload = JSON_SET(payload, '$.name', 'LooPool 体验项目'), updated_at = UTC_TIMESTAMP()
WHERE id = 'welcome' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.name')) IN ('LoopForge 体验项目', 'Bagel 体验项目');
UPDATE generic_templates SET payload = JSON_SET(payload, '$.Workflow.owner', 'LooPool'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.owner')) IN ('LoopForge', 'Bagel');
