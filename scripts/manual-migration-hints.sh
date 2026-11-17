#!/usr/bin/env bash
# Read-only check for catalog migrations an operator has to run by hand
# (they change existing SOP behaviour, so the installer never applies them).
# Prints the pending ones with the command to run; never changes data and
# never fails the caller, even when the tables do not exist yet.
# 033 is pending while a built-in code SOP has no node with id 'mr'; 034 while
# generic-backend still holds the unedited pre-034 Dockerfile, the same text
# 034 rewrites (an edited Dockerfile is left alone and is not reported).
cd "$(dirname "$0")/.." || exit 0
source scripts/docker-access.sh

pending=$(docker_access compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -N -B -u loopforge loopforge' 2>/dev/null <<'SQL'
SELECT '033_generic_sop_mr_node.sql' FROM generic_templates
WHERE id IN ('generic-backend-delivery', 'generic-frontend-delivery', 'issue-fix-production')
  AND JSON_SEARCH(payload, 'one', 'mr', NULL, '$.Workflow.nodes[*].id') IS NULL
LIMIT 1;
SELECT '034_generic_test_role_requires_tests.sql' FROM dockerfiles
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';
SQL
) || exit 0

for name in $pending; do
  printf '\n⚠ 需要手动执行 migrations/%s（内置 SOP 仍是旧版本）：\n' "$name" >&2
  printf "  docker compose exec -T mysql sh -c 'MYSQL_PWD=\"\$MYSQL_PASSWORD\" mysql -u loopforge loopforge' < migrations/%s\n" "$name" >&2
done
exit 0
