-- PostgreSQL welcome project. Same row as migrations/seed/002_welcome_project.sql
-- (MySQL): when an `admin` account exists it owns the project, otherwise the
-- project is ownerless and managed by platform administrators. Run after
-- 001_public_catalog.sql (the project points at its welcome-workflow).
-- ON CONFLICT DO NOTHING keeps an existing project, so the script is safe to
-- repeat. login_name is compared through lower() because MySQL's collation
-- matches it case-insensitively; repo.Open's idx_users_login_name_lower
-- serves the lookup.
SET client_encoding = 'UTF8';
BEGIN;
INSERT INTO projects (id,project_key,name,owner_user_id,payload,created_at,updated_at)
SELECT 'welcome','LOOP','Bagel 体验项目',o.id,
  '{"id": "welcome", "key": "LOOP", "name": "Bagel 体验项目", "description": "从创建需求、选择 SOP 到查看运行与审批，体验完整交付流程。", "color": "#3867f4", "workflowId": "welcome-workflow", "clarification": {"enabled": false}, "createdAt": "2026-10-15T00:00:00Z", "updatedAt": "2026-10-15T00:00:00Z"}'::jsonb
    || jsonb_build_object('ownerId', COALESCE(o.id, 0),
         'members', CASE WHEN o.id IS NULL THEN '[]'::jsonb ELSE jsonb_build_array(jsonb_build_object('userId', o.id, 'role', 'maintainer')) END),
  now(),now()
FROM (SELECT (SELECT id FROM users WHERE lower(login_name)='admin' LIMIT 1) AS id) AS o
ON CONFLICT DO NOTHING;
INSERT INTO project_members (project_id,user_id,role,created_at)
SELECT 'welcome',id,'maintainer',now() FROM users WHERE lower(login_name)='admin' LIMIT 1
ON CONFLICT DO NOTHING;
COMMIT;
