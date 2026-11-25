-- Idempotent MySQL migration: the built-in labels a user reads say Loopool.
--
-- The product name's capitalisation changed from "LooPool" to "Loopool"
-- (only the capital P). 044 set the welcome project to 「LooPool 体验项目」 and
-- the four built-in SOP templates' Workflow.owner to "LooPool"; this script
-- lowercases that P in the stored display values. New installs get the Loopool
-- labels from the seed. This script is not run on install or ./start.sh pull;
-- an operator runs it by hand (044 does not need to run first):
--   docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/045_loopool_lowercase_builtin_labels.sql
-- Only the shipped "LooPool" values change: a renamed welcome project and an
-- edited owner are left alone. Project SOPs already copied from these templates
-- are copies and are not touched. Internal identifiers (project key LOOP and so
-- on) are unchanged.
SET NAMES utf8mb4;
UPDATE projects SET name = 'Loopool 体验项目', updated_at = UTC_TIMESTAMP()
WHERE id = 'welcome' AND name = 'LooPool 体验项目';
UPDATE projects SET payload = JSON_SET(payload, '$.name', 'Loopool 体验项目'), updated_at = UTC_TIMESTAMP()
WHERE id = 'welcome' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.name')) = 'LooPool 体验项目';
UPDATE generic_templates SET payload = JSON_SET(payload, '$.Workflow.owner', 'Loopool'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.owner')) = 'LooPool';
