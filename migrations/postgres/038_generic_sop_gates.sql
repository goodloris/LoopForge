-- Idempotent PostgreSQL migration, the counterpart of
-- migrations/038_generic_sop_gates.sql: built-in code SOPs gate tests on an
-- exit code and send failed reviews and tests back to 开发实现. See the MySQL
-- script for what changes.
--
-- New installs get these templates from migrations/postgres/seed. An
-- installation seeded before this release keeps the old templates, and this
-- script changes the behaviour of existing SOPs, so it is not run on install
-- or ./start.sh pull; an operator runs it by hand:
--   docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U loopforge -d loopforge < migrations/postgres/038_generic_sop_gates.sql
-- Each field is rewritten only while it still holds the shipped text; edited
-- nodes, prompts and descriptions are left alone. Project SOPs already adopted
-- from these templates are copies and are not touched.
BEGIN;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Workflow,description}', '"后端需求从需求定型到独立评审与交付。仓库目标分支有 .loopforge/verify 时，测试按它的退出码把关，未通过打回开发实现；没有时测试结论由 Agent 判定。"'::jsonb), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #> '{Workflow,description}' = '"后端需求从需求定型到独立评审与交付。"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,3,maxReworks}', '2'), '{Workflow,nodes,3,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #> '{Workflow,nodes,3}' = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-backend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,4,maxReworks}', '2'), '{Workflow,nodes,4,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #> '{Workflow,nodes,4}' = '{"id": "test", "name": "测试验证", "type": "agent", "agentId": "generic-backend-delivery-test", "maxAttempts": 2, "position": {"x": 1080, "y": 150}, "artifactOutputs": [{"name": "test", "path": "test-report.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,2,prompt}', '"工作领域：后端服务、数据与 API。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。仓库目标分支有 .loopforge/verify 时，完成前在仓库目录运行它并让退出码为 0：测试验证节点按它的退出码判定，不为 0 会打回本节点。被打回时先读上一轮反馈里的命令输出并据此修复。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #>> '{Agents,2,id}' = 'generic-backend-delivery-implement'
  AND payload::jsonb #> '{Agents,2,prompt}' = '"工作领域：后端服务、数据与 API。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,3,prompt}', '"工作领域：后端服务、数据与 API。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT，并提交 rework，平台会打回开发实现。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #>> '{Agents,3,id}' = 'generic-backend-delivery-review'
  AND payload::jsonb #> '{Agents,3,prompt}' = '"工作领域：后端服务、数据与 API。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Agents,4,prompt}', '"工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。本节点提交 pass 后，平台在每个仓库运行目标分支上的 .loopforge/verify，按退出码判定，不为 0 时打回开发实现；仓库没有这个文件时，在 test-report.md 写明「未配置验证命令（.loopforge/verify），测试结论由 Agent 判定」。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), '{Agents,4,verification}', '[{"id": "loopforge-verify", "name": "验证命令（.loopforge/verify）", "mode": "command", "stage": "after_execution", "commands": ["fail=0\nfound=0\ntab=\"$(printf ''\\t'')\"\nscript=\"$(mktemp)\"\nrun_verify() {\n  found=1\n  printf ''$ %s（目录 %s）\\n'' \"$2\" \"$1\"\n  case \"$(head -n 1 \"$3\")\" in\n    ''#!''*) interp=\"$(head -n 1 \"$3\" | cut -c3-)\" ;;\n    *) interp=sh ;;\n  esac\n  (cd \"$1\" && $interp \"$3\" </dev/null)\n  rc=$?\n  printf ''exit=%s\\n'' \"$rc\"\n  [ \"$rc\" = 0 ] || fail=1\n}\nwhile IFS=\"$tab\" read -r d work base; do\n  [ -n \"$d\" ] || continue\n  if ! git -C \"$d\" cat-file -e \"refs/remotes/origin/$base:.loopforge/verify\" 2>/dev/null; then\n    printf ''%s：目标分支 %s 上没有 .loopforge/verify\\n'' \"$d\" \"$base\"\n    continue\n  fi\n  if ! git -C \"$d\" show \"refs/remotes/origin/$base:.loopforge/verify\" > \"$script\"; then\n    printf ''%s：读取 origin/%s:.loopforge/verify 失败\\n'' \"$d\" \"$base\"\n    fail=1\n    continue\n  fi\n  run_verify \"$d\" \"origin/$base:.loopforge/verify\" \"$script\"\ndone <<EOF\n$AGENTFLOW_WORK_BRANCHES\nEOF\nif [ -z \"$AGENTFLOW_WORK_BRANCHES\" ] && [ -f .loopforge/verify ]; then\n  cp .loopforge/verify \"$script\" && run_verify . .loopforge/verify \"$script\"\nfi\nrm -f \"$script\"\n[ \"$found\" = 1 ] || printf ''未配置验证命令（.loopforge/verify），测试结论由 Agent 判定\\n''\nexit \"$fail\""], "required": true, "onFailure": "reject", "executionScope": "run"}]'::jsonb), updated_at = now()
WHERE id = 'generic-backend-delivery'
  AND payload::jsonb #>> '{Agents,4,id}' = 'generic-backend-delivery-test'
  AND payload::jsonb #> '{Agents,4,verification}' IS NULL
  AND payload::jsonb #> '{Agents,4,prompt}' = '"工作领域：后端服务、数据与 API。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Workflow,description}', '"前端需求从页面设计到交互与回归验证。仓库目标分支有 .loopforge/verify 时，测试按它的退出码把关，未通过打回开发实现；没有时测试结论由 Agent 判定。"'::jsonb), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #> '{Workflow,description}' = '"前端需求从页面设计到交互与回归验证。"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,3,maxReworks}', '2'), '{Workflow,nodes,3,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #> '{Workflow,nodes,3}' = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "generic-frontend-delivery-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,4,maxReworks}', '2'), '{Workflow,nodes,4,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #> '{Workflow,nodes,4}' = '{"id": "test", "name": "测试验证", "type": "agent", "agentId": "generic-frontend-delivery-test", "maxAttempts": 2, "position": {"x": 1080, "y": 150}, "artifactOutputs": [{"name": "test", "path": "test-report.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,2,prompt}', '"工作领域：前端页面、交互、可访问性与响应式。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。仓库目标分支有 .loopforge/verify 时，完成前在仓库目录运行它并让退出码为 0：测试验证节点按它的退出码判定，不为 0 会打回本节点。被打回时先读上一轮反馈里的命令输出并据此修复。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #>> '{Agents,2,id}' = 'generic-frontend-delivery-implement'
  AND payload::jsonb #> '{Agents,2,prompt}' = '"工作领域：前端页面、交互、可访问性与响应式。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,3,prompt}', '"工作领域：前端页面、交互、可访问性与响应式。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT，并提交 rework，平台会打回开发实现。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #>> '{Agents,3,id}' = 'generic-frontend-delivery-review'
  AND payload::jsonb #> '{Agents,3,prompt}' = '"工作领域：前端页面、交互、可访问性与响应式。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Agents,4,prompt}', '"工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。本节点提交 pass 后，平台在每个仓库运行目标分支上的 .loopforge/verify，按退出码判定，不为 0 时打回开发实现；仓库没有这个文件时，在 test-report.md 写明「未配置验证命令（.loopforge/verify），测试结论由 Agent 判定」。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), '{Agents,4,verification}', '[{"id": "loopforge-verify", "name": "验证命令（.loopforge/verify）", "mode": "command", "stage": "after_execution", "commands": ["fail=0\nfound=0\ntab=\"$(printf ''\\t'')\"\nscript=\"$(mktemp)\"\nrun_verify() {\n  found=1\n  printf ''$ %s（目录 %s）\\n'' \"$2\" \"$1\"\n  case \"$(head -n 1 \"$3\")\" in\n    ''#!''*) interp=\"$(head -n 1 \"$3\" | cut -c3-)\" ;;\n    *) interp=sh ;;\n  esac\n  (cd \"$1\" && $interp \"$3\" </dev/null)\n  rc=$?\n  printf ''exit=%s\\n'' \"$rc\"\n  [ \"$rc\" = 0 ] || fail=1\n}\nwhile IFS=\"$tab\" read -r d work base; do\n  [ -n \"$d\" ] || continue\n  if ! git -C \"$d\" cat-file -e \"refs/remotes/origin/$base:.loopforge/verify\" 2>/dev/null; then\n    printf ''%s：目标分支 %s 上没有 .loopforge/verify\\n'' \"$d\" \"$base\"\n    continue\n  fi\n  if ! git -C \"$d\" show \"refs/remotes/origin/$base:.loopforge/verify\" > \"$script\"; then\n    printf ''%s：读取 origin/%s:.loopforge/verify 失败\\n'' \"$d\" \"$base\"\n    fail=1\n    continue\n  fi\n  run_verify \"$d\" \"origin/$base:.loopforge/verify\" \"$script\"\ndone <<EOF\n$AGENTFLOW_WORK_BRANCHES\nEOF\nif [ -z \"$AGENTFLOW_WORK_BRANCHES\" ] && [ -f .loopforge/verify ]; then\n  cp .loopforge/verify \"$script\" && run_verify . .loopforge/verify \"$script\"\nfi\nrm -f \"$script\"\n[ \"$found\" = 1 ] || printf ''未配置验证命令（.loopforge/verify），测试结论由 Agent 判定\\n''\nexit \"$fail\""], "required": true, "onFailure": "reject", "executionScope": "run"}]'::jsonb), updated_at = now()
WHERE id = 'generic-frontend-delivery'
  AND payload::jsonb #>> '{Agents,4,id}' = 'generic-frontend-delivery-test'
  AND payload::jsonb #> '{Agents,4,verification}' IS NULL
  AND payload::jsonb #> '{Agents,4,prompt}' = '"工作领域：前端页面、交互、可访问性与响应式。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Workflow,description}', '"先复现，再修复，独立评审并回归。仓库目标分支有 .loopforge/verify 时，测试按它的退出码把关，未通过打回开发实现；没有时测试结论由 Agent 判定。"'::jsonb), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #> '{Workflow,description}' = '"先复现，再修复，独立评审并回归。"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,3,maxReworks}', '2'), '{Workflow,nodes,3,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #> '{Workflow,nodes,3}' = '{"id": "review", "name": "独立评审", "type": "agent", "agentId": "issue-fix-production-review", "maxAttempts": 2, "position": {"x": 835, "y": 150}, "artifactOutputs": [{"name": "review", "path": "review.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Workflow,nodes,4,maxReworks}', '2'), '{Workflow,nodes,4,rejectTo}', '"implement"'), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #> '{Workflow,nodes,4}' = '{"id": "test", "name": "测试验证", "type": "agent", "agentId": "issue-fix-production-test", "maxAttempts": 2, "position": {"x": 1080, "y": 150}, "artifactOutputs": [{"name": "test", "path": "test-report.md", "mediaType": "text/markdown", "required": true}]}'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,2,prompt}', '"工作领域：缺陷根因、复现与回归。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。仓库目标分支有 .loopforge/verify 时，完成前在仓库目录运行它并让退出码为 0：测试验证节点按它的退出码判定，不为 0 会打回本节点。被打回时先读上一轮反馈里的命令输出并据此修复。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #>> '{Agents,2,id}' = 'issue-fix-production-implement'
  AND payload::jsonb #> '{Agents,2,prompt}' = '"工作领域：缺陷根因、复现与回归。依据 requirement.md 和 design.md 实现需求，保持范围最小；执行适用验证，记录实现、改动和验证到 implementation.md。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(payload::jsonb, '{Agents,3,prompt}', '"工作领域：缺陷根因、复现与回归。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT，并提交 rework，平台会打回开发实现。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #>> '{Agents,3,id}' = 'issue-fix-production-review'
  AND payload::jsonb #> '{Agents,3,prompt}' = '"工作领域：缺陷根因、复现与回归。独立审查改动及测试证据；按严重程度列出有位置与影响的缺陷及修复建议到 review.md。发现阻断问题必须明确写出 REJECT。不得伪造通过结论。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
UPDATE generic_templates
SET payload = jsonb_set(jsonb_set(payload::jsonb, '{Agents,4,prompt}', '"工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。本节点提交 pass 后，平台在每个仓库运行目标分支上的 .loopforge/verify，按退出码判定，不为 0 时打回开发实现；仓库没有这个文件时，在 test-report.md 写明「未配置验证命令（.loopforge/verify），测试结论由 Agent 判定」。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb), '{Agents,4,verification}', '[{"id": "loopforge-verify", "name": "验证命令（.loopforge/verify）", "mode": "command", "stage": "after_execution", "commands": ["fail=0\nfound=0\ntab=\"$(printf ''\\t'')\"\nscript=\"$(mktemp)\"\nrun_verify() {\n  found=1\n  printf ''$ %s（目录 %s）\\n'' \"$2\" \"$1\"\n  case \"$(head -n 1 \"$3\")\" in\n    ''#!''*) interp=\"$(head -n 1 \"$3\" | cut -c3-)\" ;;\n    *) interp=sh ;;\n  esac\n  (cd \"$1\" && $interp \"$3\" </dev/null)\n  rc=$?\n  printf ''exit=%s\\n'' \"$rc\"\n  [ \"$rc\" = 0 ] || fail=1\n}\nwhile IFS=\"$tab\" read -r d work base; do\n  [ -n \"$d\" ] || continue\n  if ! git -C \"$d\" cat-file -e \"refs/remotes/origin/$base:.loopforge/verify\" 2>/dev/null; then\n    printf ''%s：目标分支 %s 上没有 .loopforge/verify\\n'' \"$d\" \"$base\"\n    continue\n  fi\n  if ! git -C \"$d\" show \"refs/remotes/origin/$base:.loopforge/verify\" > \"$script\"; then\n    printf ''%s：读取 origin/%s:.loopforge/verify 失败\\n'' \"$d\" \"$base\"\n    fail=1\n    continue\n  fi\n  run_verify \"$d\" \"origin/$base:.loopforge/verify\" \"$script\"\ndone <<EOF\n$AGENTFLOW_WORK_BRANCHES\nEOF\nif [ -z \"$AGENTFLOW_WORK_BRANCHES\" ] && [ -f .loopforge/verify ]; then\n  cp .loopforge/verify \"$script\" && run_verify . .loopforge/verify \"$script\"\nfi\nrm -f \"$script\"\n[ \"$found\" = 1 ] || printf ''未配置验证命令（.loopforge/verify），测试结论由 Agent 判定\\n''\nexit \"$fail\""], "required": true, "onFailure": "reject", "executionScope": "run"}]'::jsonb), updated_at = now()
WHERE id = 'issue-fix-production'
  AND payload::jsonb #>> '{Agents,4,id}' = 'issue-fix-production-test'
  AND payload::jsonb #> '{Agents,4,verification}' IS NULL
  AND payload::jsonb #> '{Agents,4,prompt}' = '"工作领域：缺陷根因、复现与回归。根据验收标准执行项目真实测试、构建及必要端到端场景。必要时修复问题并复测。将命令、退出码、结果、未验证范围写到 test-report.md。无法实际运行项目测试（例如缺少测试工具且无法安装）时，test-report.md 的结论必须写「未通过」并提交 fail 让本节点失败，不得用手工验证替代自动化测试。\n\n完成本节点约定的工作并交付产物。\n\n### 输入\n\n- 任务需求：{{input}}\n- 仓库上下文：{{repository}}"'::jsonb;
COMMIT;
