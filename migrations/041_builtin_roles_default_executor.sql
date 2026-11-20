-- Idempotent MySQL migration: built-in roles follow the deployment's default
-- executor instead of naming CodeBuddy.
--
-- The roles of the four built-in SOPs and the shared 基线对齐执行员 used to
-- store executorId "codebuddy". A role without executorId runs on the
-- deployment's default CLI (AGENTFLOW_DEFAULT_EXECUTOR, CodeBuddy when unset),
-- so a deployment that defaults to CodeBuddy behaves exactly as before and one
-- that defaults to another CLI (for example Kiro CLI) no longer needs a
-- CodeBuddy key to run the built-in SOPs.
--
-- New installs get these definitions from the seed. This script changes the
-- executor of existing built-in roles, so it is not run on install or
-- ./start.sh pull; an operator runs it by hand, after 038 and any 039–040:
--   docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/041_builtin_roles_default_executor.sql
-- Only executorId "codebuddy" is removed; a role switched to another CLI keeps
-- it. Project SOPs and roles already copied from these templates are copies
-- and are not touched.
SET NAMES utf8mb4;
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[0].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[0].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[1].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[1].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[2].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[2].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[3].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[3].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[4].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[5].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[5].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[6].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[6].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[7].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[7].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[8].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[8].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[9].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[9].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[10].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[10].executorId')) = 'codebuddy';
UPDATE generic_templates SET payload = JSON_REMOVE(payload, '$.Agents[11].executorId'), updated_at = UTC_TIMESTAMP()
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[11].executorId')) = 'codebuddy';
UPDATE agents SET payload = JSON_REMOVE(payload, '$.executorId'), updated_at = UTC_TIMESTAMP()
WHERE id = 'baseline-align-resolver'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.executorId')) = 'codebuddy';
