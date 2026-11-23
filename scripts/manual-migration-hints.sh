#!/usr/bin/env bash
# Read-only check for migrations an operator has to run by hand (they change
# existing data or SOP behaviour, so the installer never applies them).
# Prints the pending ones with the command to run; never changes data and
# never fails the caller, even when the tables do not exist yet. Each check is
# its own query, so one that fails (an older schema) hides no other.
#
# MySQL: 037_generic_test_role_requires_tests while a built-in 测试验证 role
# still holds the unedited pre-037 prompt; 038_generic_sop_gates while a
# built-in code SOP still has the shipped 独立评审 node without rejectTo.
# Both databases: 036_git_http_credentials while a user's plain Git token has
# no github.com HTTPS credential; 037_retire_plain_git_token while a plain
# token is still stored next to an identical github.com credential (run it
# only after checking the 036 backfill); 039_schedule_execution_legacy_stats
# while a finished execution of the last 30 days has no hourly bucket; 041
# while a built-in role still names executorId codebuddy; 042 while the
# welcome project is still 「LoopForge 体验项目」 or a built-in SOP's owner is
# LoopForge; 044 while either still holds the LoopForge or Bagel label. 033 and 038_generic_sop_host_credentials are replayed by
# scripts/install-schema-extensions.sh and need no hint. PostgreSQL
# installations started from a seed that already holds 033 and 037.
#
# The SQL is read with `read -d ''` instead of a heredoc inside $(...):
# bash 3.2 (macOS /bin/bash) cannot parse quotes and parentheses there.
cd "$(dirname "$0")/.." || exit 0
source scripts/docker-access.sh
source scripts/database.sh

case "$LOOPFORGE_DATABASE" in postgres|mysql) ;; *) exit 0 ;; esac

pending_reason() {
  case "$1" in
    036_git_http_credentials.sql) echo '有用户的 GitHub Token 还没有迁移为按主机的 HTTPS Git 凭据；升级后旧 Token 不再生效' ;;
    037_retire_plain_git_token.sql) echo '旧的 GitHub Token 已有相同的 HTTPS 凭据副本，核对 036 的回填后再清理' ;;
    039_schedule_execution_legacy_stats.sql) echo '定时任务的历史执行缺少小时统计；需停服并备份后执行' ;;
    *) echo '内置 SOP 仍是旧版本' ;;
  esac
}

report() {
  local name=$1 dir=migrations
  [[ "$LOOPFORGE_DATABASE" == postgres ]] && dir=migrations/postgres
  printf '\n⚠ 需要手动执行 %s/%s（%s）：\n' "$dir" "$name" "$(pending_reason "$name")" >&2
  if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then
    printf '  docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < %s/%s\n' "$dir" "$name" >&2
  else
    printf "  docker compose exec -T mysql sh -c 'MYSQL_PWD=\"\$MYSQL_PASSWORD\" mysql -u loopforge loopforge' < %s/%s\n" "$dir" "$name" >&2
  fi
}

# check NAME reads one SELECT from stdin; NAME is pending when it returns a
# row. A query that fails (a table an older schema lacks) reports nothing, and
# each name is reported once even when several checks cover it.
reported=' '
check() {
  local name=$1 sql out
  read -r -d '' sql || true
  [[ "$reported" == *" $name "* ]] && return 0
  [[ "$LOOPFORGE_DATABASE" == mysql ]] && sql="SET NAMES utf8mb4;
$sql"
  out=$(printf '%s\n' "$sql" | db_query 2>/dev/null) || return 0
  if [[ -n "$out" ]]; then
    report "$name"
    reported="$reported$name "
  fi
  return 0
}

if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then
check 038_generic_sop_gates.sql <<'SQL'
SELECT 1 FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND ((payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-backend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb
   OR (payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-frontend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb
   OR (payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "issue-fix-production-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb)
LIMIT 1;
SQL
# Before the upgrade the credential table does not exist yet.
check 036_git_http_credentials.sql <<'SQL'
SELECT 1 FROM user_configs c
WHERE to_regclass('user_git_http_credentials') IS NULL
  AND COALESCE(btrim(c.git_token), '') <> ''
  AND COALESCE(btrim(c.gongfeng_refresh_token), '') = ''
  AND lower(btrim(c.git_token)) NOT LIKE 'oauth2:%'
  AND lower(btrim(c.git_token)) NOT LIKE 'private:%'
LIMIT 1;
SQL
check 036_git_http_credentials.sql <<'SQL'
SELECT 1 FROM user_configs c
WHERE COALESCE(btrim(c.git_token), '') <> ''
  AND COALESCE(btrim(c.gongfeng_refresh_token), '') = ''
  AND lower(btrim(c.git_token)) NOT LIKE 'oauth2:%'
  AND lower(btrim(c.git_token)) NOT LIKE 'private:%'
  AND NOT EXISTS (SELECT 1 FROM user_git_http_credentials h
    WHERE h.user_id = c.user_id AND h.id = encode(sha256(convert_to('github.com', 'UTF8')), 'hex'))
LIMIT 1;
SQL
check 037_retire_plain_git_token.sql <<'SQL'
SELECT 1 FROM user_configs c
JOIN user_git_http_credentials h ON h.user_id = c.user_id
  AND h.id = encode(sha256(convert_to('github.com', 'UTF8')), 'hex') AND h.host = 'github.com'
WHERE COALESCE(btrim(c.git_token), '') <> ''
  AND COALESCE(btrim(c.gongfeng_refresh_token), '') = ''
  AND lower(btrim(c.git_token)) NOT LIKE 'oauth2:%'
  AND lower(btrim(c.git_token)) NOT LIKE 'private:%'
  AND h.token = btrim(c.git_token)
LIMIT 1;
SQL
check 039_schedule_execution_legacy_stats.sql <<'SQL'
SELECT 1 FROM schedule_job_executions e
WHERE e.started_at >= now() - interval '30 days'
  AND COALESCE(e.status, '') NOT IN ('', 'running')
  AND NOT EXISTS (SELECT 1 FROM schedule_job_execution_stats s
    WHERE s.job_id = e.job_id AND s.bucket_hour = date_trunc('hour', e.started_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
LIMIT 1;
SQL
check 041_builtin_roles_default_executor.sql <<'SQL'
SELECT 1 FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND payload::jsonb -> 'Agents' @> '[{"executorId": "codebuddy"}]'::jsonb
LIMIT 1;
SQL
check 042_bagel_builtin_labels.sql <<'SQL'
SELECT 1
WHERE EXISTS (SELECT 1 FROM projects WHERE id = 'welcome' AND name = 'LoopForge 体验项目')
   OR EXISTS (SELECT 1 FROM generic_templates WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
     AND payload::jsonb #>> '{Workflow,owner}' = 'LoopForge');
SQL
check 044_loopool_builtin_labels.sql <<'SQL'
SELECT 1
WHERE EXISTS (SELECT 1 FROM projects WHERE id = 'welcome' AND name IN ('LoopForge 体验项目', 'Bagel 体验项目'))
   OR EXISTS (SELECT 1 FROM generic_templates WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
     AND payload::jsonb #>> '{Workflow,owner}' IN ('LoopForge', 'Bagel'));
SQL
exit 0
fi

check 037_generic_test_role_requires_tests.sql <<'SQL'
SELECT 1 FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND (JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'
   OR JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'
   OR JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}')
LIMIT 1;
SQL
check 038_generic_sop_gates.sql <<'SQL'
SELECT 1 FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND (JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-backend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON)
   OR JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-frontend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON)
   OR JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "issue-fix-production-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON))
LIMIT 1;
SQL
# Before the upgrade the credential table does not exist yet.
check 036_git_http_credentials.sql <<'SQL'
SELECT 1 FROM user_configs c
WHERE NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'user_git_http_credentials')
  AND COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%'
LIMIT 1;
SQL
check 036_git_http_credentials.sql <<'SQL'
SELECT 1 FROM user_configs c
WHERE COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%'
  AND NOT EXISTS (SELECT 1 FROM user_git_http_credentials h
    WHERE h.user_id = c.user_id AND h.id = SHA2('github.com', 256))
LIMIT 1;
SQL
check 037_retire_plain_git_token.sql <<'SQL'
SELECT 1 FROM user_configs c
JOIN user_git_http_credentials h ON h.user_id = c.user_id
  AND h.id = SHA2('github.com', 256) AND CAST(h.host AS BINARY) = 'github.com'
WHERE COALESCE(TRIM(c.git_token), '') <> ''
  AND COALESCE(TRIM(c.gongfeng_refresh_token), '') = ''
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'oauth2:%'
  AND LOWER(TRIM(c.git_token)) NOT LIKE 'private:%'
  AND CAST(h.token AS BINARY) = CAST(TRIM(c.git_token) AS BINARY)
LIMIT 1;
SQL
check 039_schedule_execution_legacy_stats.sql <<'SQL'
SELECT 1 FROM schedule_job_executions e
WHERE e.started_at >= UTC_TIMESTAMP() - INTERVAL 30 DAY
  AND COALESCE(e.status, '') NOT IN ('', 'running')
  AND NOT EXISTS (SELECT 1 FROM schedule_job_execution_stats s
    WHERE s.job_id = e.job_id AND s.bucket_hour = CAST(DATE_FORMAT(e.started_at, '%Y-%m-%d %H:00:00') AS DATETIME))
LIMIT 1;
SQL
check 041_builtin_roles_default_executor.sql <<'SQL'
SELECT 1 FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
  AND JSON_CONTAINS(JSON_EXTRACT(payload, '$.Agents[*].executorId'), '"codebuddy"')
LIMIT 1;
SQL
check 042_bagel_builtin_labels.sql <<'SQL'
SELECT 1 FROM DUAL
WHERE EXISTS (SELECT 1 FROM projects WHERE id = 'welcome' AND name = 'LoopForge 体验项目')
   OR EXISTS (SELECT 1 FROM generic_templates WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
     AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.owner')) = 'LoopForge');
SQL
check 044_loopool_builtin_labels.sql <<'SQL'
SELECT 1 FROM DUAL
WHERE EXISTS (SELECT 1 FROM projects WHERE id = 'welcome' AND name IN ('LoopForge 体验项目', 'Bagel 体验项目'))
   OR EXISTS (SELECT 1 FROM generic_templates WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production', 'generic-product-delivery')
     AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Workflow.owner')) IN ('LoopForge', 'Bagel'));
SQL
exit 0
