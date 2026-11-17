-- Idempotent MySQL migration: stop the built-in test roles from passing
-- without running tests, and give the shared sandbox pip.
--
-- 1. The generic-backend Dockerfile gains python3-pip and python3-venv, so a
--    test role can install a Python project's test tools (pytest and so on)
--    instead of falling back to manual checks. The platform image
--    (Dockerfile) and runtime.IsolatedDockerfile carry the same packages; the
--    row has to match IsolatedDockerfile byte for byte, otherwise a
--    generic-docker sandbox builds a separate agentflow-img-* image.
-- 2. The 测试验证 role of the backend, frontend and defect-fix SOPs must write
--    「未通过」 and submit fail when it cannot run the project's tests, instead
--    of passing on manual verification.
--
-- New installs get both from the seed. This script changes the behaviour of
-- existing SOPs, so it is not run on install or ./start.sh pull; an operator
-- runs it by hand after upgrading to a platform image that has python3-pip:
--   docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/034_generic_test_role_requires_tests.sql
-- Only the unedited shipped text is rewritten: an edited Dockerfile or test
-- prompt is left alone. Project SOPs already adopted from these templates are
-- copies and are not touched.
SET NAMES utf8mb4;
START TRANSACTION;
UPDATE dockerfiles
SET payload = JSON_SET(payload, '$.content', 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 python3-pip python3-venv ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n')
WHERE id = 'generic-backend'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.content')) = 'FROM node:22-bookworm\nRUN apt-get update && apt-get install -y --no-install-recommends git openssh-client python3 ripgrep ca-certificates curl && rm -rf /var/lib/apt/lists/*\nRUN npm install -g @openai/codex @anthropic-ai/claude-code @tencent-ai/codebuddy-code\nWORKDIR /workspace\n';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].prompt', '工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-backend-delivery'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'generic-backend-delivery-test'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].prompt', '工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'generic-frontend-delivery'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'generic-frontend-delivery-test'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
UPDATE generic_templates
SET payload = JSON_SET(payload, '$.Agents[4].prompt', '工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}'), updated_at = UTC_TIMESTAMP()
WHERE id = 'issue-fix-production'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].id')) = 'issue-fix-production-test'
  AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.Agents[4].prompt')) = '工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}';
COMMIT;
