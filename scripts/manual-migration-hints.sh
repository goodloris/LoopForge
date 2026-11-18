#!/usr/bin/env bash
# Read-only check for catalog migrations an operator has to run by hand
# (they change existing SOP behaviour, so the installer never applies them).
# Prints the pending ones with the command to run; never changes data and
# never fails the caller, even when the tables do not exist yet.
# 033 is pending while a built-in code SOP has no node with id 'mr'; 037 while
# a built-in 测试验证 role still holds the unedited pre-037 prompt, the text 037
# rewrites (an edited prompt is left alone and is not reported).
# PostgreSQL has nothing to report: it is supported for new installations
# only, and its seed already holds the 033 node and the 037 prompt.
cd "$(dirname "$0")/.." || exit 0
source scripts/docker-access.sh
source scripts/database.sh
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
SQL
) || exit 0

for name in $pending; do
  printf '\n⚠ 需要手动执行 migrations/%s（内置 SOP 仍是旧版本）：\n' "$name" >&2
  printf "  docker compose exec -T mysql sh -c 'MYSQL_PWD=\"\$MYSQL_PASSWORD\" mysql -u loopforge loopforge' < migrations/%s\n" "$name" >&2
done
exit 0
