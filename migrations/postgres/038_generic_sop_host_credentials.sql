-- Idempotent PostgreSQL counterpart of
-- migrations/038_generic_sop_host_credentials.sql (upstream): the 「发起 MR」
-- roles of the built-in code SOPs stop reading $AGENTFLOW_GIT_TOKEN and get a
-- token for the repository host from git credential fill instead.
--
-- New installs get this text from migrations/postgres/seed. Only the shipped
-- sentence is replaced, in the role whose id is the template id plus "-mr";
-- an edited prompt without it is left alone, so a second run changes nothing.
-- scripts/install-schema-extensions.sh runs it on install and ./start.sh pull,
-- like the MySQL script.
BEGIN;
UPDATE generic_templates t
SET payload = jsonb_set(t.payload::jsonb, '{Agents}', (
      SELECT jsonb_agg(CASE
          WHEN a ->> 'id' = t.id || '-mr'
           AND strpos(a ->> 'prompt', 'GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。') > 0
          THEN jsonb_set(a, '{prompt}', to_jsonb(replace(a ->> 'prompt',
            'GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。',
            '使用托管平台的 CLI（如 gh、glab）或已配置的 MCP，也可查阅该平台官方 API。调用 API 需要令牌时，用 git credential fill 按仓库主机获取，只在进程内存中使用。')))
          ELSE a END ORDER BY i)
      FROM jsonb_array_elements(t.payload::jsonb -> 'Agents') WITH ORDINALITY AS x(a, i))),
    updated_at = now()
WHERE t.id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND EXISTS (
    SELECT 1 FROM jsonb_array_elements(t.payload::jsonb -> 'Agents') a
    WHERE a ->> 'id' = t.id || '-mr'
      AND strpos(a ->> 'prompt', 'GitHub 仓库优先用已配置的 GitHub MCP，没有时用 curl 调 GitHub API，令牌取 $AGENTFLOW_GIT_TOKEN；其它托管平台用已配置的对应 MCP。') > 0);
COMMIT;
