#!/usr/bin/env bash
# Read-only check for catalog migrations an operator has to run by hand
# (they change existing SOP behaviour, so the installer never applies them).
# Prints the pending ones with the command to run; never changes data and
# never fails the caller, even when the tables do not exist yet.
# 033 is pending while a built-in code SOP has no node with id 'mr'; 037 while
# a built-in 测试验证 role still holds the unedited pre-037 prompt, the text 037
# rewrites (an edited prompt is left alone and is not reported); 038 while a
# built-in code SOP still has the shipped 独立评审 node without rejectTo.
# PostgreSQL installations started from a seed that already holds 033 and 037,
# so only 038 can be pending there.
cd "$(dirname "$0")/.." || exit 0
source scripts/docker-access.sh
source scripts/database.sh

if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then
  pending=$(db_query 2>/dev/null <<'SQL'
SELECT '038_generic_sop_gates.sql' FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND ((payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-backend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb
   OR (payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-frontend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb
   OR (payload::jsonb -> 'Workflow' -> 'nodes' -> 3) = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "issue-fix-production-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb)
LIMIT 1;
SQL
  ) || exit 0
  for name in $pending; do
    printf '\n⚠ 需要手动执行 migrations/postgres/%s（内置 SOP 仍是旧版本）：\n' "$name" >&2
    printf '  docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/%s\n' "$name" >&2
  done
  exit 0
fi
[[ "$LOOPFORGE_DATABASE" == mysql ]] || exit 0

pending=$(db_query 2>/dev/null <<'SQL'
SET NAMES utf8mb4;
SELECT '033_generic_sop_mr_node.sql' FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND JSON_SEARCH(payload, 'one', 'mr', NULL, '$.Workflow.nodes[*].id') IS NULL
LIMIT 1;
SELECT '037_generic_test_role_requires_tests.sql' FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND (JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'
   OR JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'
   OR JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}')
LIMIT 1;
SELECT '038_generic_sop_gates.sql' FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND (JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-backend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON)
   OR JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-frontend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON)
   OR JSON_EXTRACT(payload, '$.Workflow.nodes[3]') = CAST('{"id": "review", "name": "独立评审", "type": "agent", "agentId": "issue-fix-production-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}' AS JSON))
LIMIT 1;
SQL
) || exit 0

for name in $pending; do
  printf '\n⚠ 需要手动执行 migrations/%s（内置 SOP 仍是旧版本）：\n' "$name" >&2
  printf "  docker compose exec -T mysql sh -c 'MYSQL_PWD=\"\$MYSQL_PASSWORD\" mysql -u loopforge loopforge' < migrations/%s\n" "$name" >&2
done
exit 0
