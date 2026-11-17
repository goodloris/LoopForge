# PostgreSQL 迁移与 seed

PostgreSQL 只支持全新安装，不迁移存量数据。表、列和普通索引由 gorm 建：`repo.Open` 每次启动都会执行 AutoMigrate 增量 DDL。本目录只放 gorm 建不出来的结构和 seed。和 `migrations/` 上层的 MySQL 脚本一样，这些脚本由运维手动执行或由安装脚本执行，平台启动时不会执行。

```
postgres/
  001_baseline_extensions.sql   # gorm 建不出来的结构：001 的生成列及索引
  seed/
    001_public_catalog.sql      # 公共目录，等价于 ../seed/001_public_catalog.sql
    002_welcome_project.sql     # 体验项目，等价于 ../seed/002_welcome_project.sql
  README.md
```

## 执行顺序

1. 用 `AGENTFLOW_DATABASE_DSN=postgres://...` 启动一次平台，等健康检查通过（gorm 建出全部表）。
2. 执行 `001_baseline_extensions.sql`。
3. 依次执行 `seed/001_public_catalog.sql`、`seed/002_welcome_project.sql`。

```bash
for f in 001_baseline_extensions.sql seed/001_public_catalog.sql seed/002_welcome_project.sql; do
  psql "$DSN" -v ON_ERROR_STOP=1 -f "migrations/postgres/$f"
done
```

三个脚本都可以重复执行：DDL 都带 `IF NOT EXISTS`，seed 用 `INSERT ... ON CONFLICT DO NOTHING`，已存在的行（包括运维改过的定义）保持不变。体验项目在 `admin` 账号存在时归它所有，所以先建管理员再执行 `002`，项目就有负责人；之后再执行不会改变已有项目。

数据库编码必须是 UTF8（seed 里有中文）。要求 PostgreSQL 12 或更高版本（STORED 生成列）。

## 约定

- **seed 必须和 MySQL seed 同步修改。** `internal/repo/pg_migrations_test.go` 的 `TestPostgresSeedMatchesMySQLSeed` 逐行比较两边的表、列和解码后的 JSON，只改一边时测试会失败。这个测试不连数据库，每次 `go test` 都会运行。
- seed 的字符串按 PG 默认的 `standard_conforming_strings` 书写：反斜杠不转义，JSON 里的 `\n`、`\"` 只写一个反斜杠；MySQL 版写成 `\\n`、`\\"`。单引号两边都写成 `''`。
- 以后改动已有数据（MySQL 那边的 `0NN_*.sql` 数据修补）时，PG 版本按序号在本目录新增 `002_*.sql`，和 MySQL 脚本放在同一个提交里。新安装直接用 seed 拿到最新内容，所以 seed 也要同步改。
- `TestPostgresBaselineAndSeed` 在设置 `AGENTFLOW_TEST_PG_DSN` 时运行：在 gorm 建出的 schema 上按上面的顺序执行两遍，检查行数、jsonb 能否接受 payload、生成列取值、索引、体验项目负责人，最后用生产 `Open` 重新打开，确认增量 DDL 能接受这些额外结构。子用例 `payload jsonb` 先把 `run_telemetry`、`run_logs` 的 payload 改成 jsonb 再执行 baseline，确认生成列在 json 和 jsonb 上都能建成。

## MySQL 001–035 在 PG 全新安装下的结论

判断依据：在 PG 17 上用 gorm 建表后，逐个比对每个脚本创建的表、列和索引名（`information_schema`、`pg_indexes`），并用 grep 确认代码是否还引用相关表。

| 脚本 | 内容 | PG 结论 |
|---|---|---|
| 001_run_obs_generated_columns | `run_telemetry.adapter`、`run_logs.level` 生成列和索引 | **需要等价物**：`001_baseline_extensions.sql` 第 1 节 |
| 002_knowledge_base | 知识库两张表 | 不需要，gorm 已建（含 4 个索引） |
| 003_external_workflow_usage | insight 两张表 | 不需要，gorm 已建 |
| 004_notification_bots | Bot 与通知投递表 | 不需要，gorm 已建（含 `idx_notification_due`） |
| 005_notification_sources | 投递来源列 | 不需要，gorm 已建（`source_type` 默认值 `'run'` 一致） |
| 006_notification_waiting_policy | `waiting_policy` 列 | 不需要，gorm 已建（默认值 `'actionable'` 一致） |
| 007_bot_interactions | Bot 会话表、`target_type` 列 | 不需要，gorm 已建（含 `idx_bot_queue`） |
| 007_notification_reports | 报告列 | 不需要，gorm 已建 |
| 008_bot_user_bindings | 绑定表 | 不需要，gorm 已建 |
| 009_bot_interactions_utf8mb4 | MySQL 字符集转换 | 不需要，PG 库编码为 UTF8 |
| 010_project_run_admission | 准入锁表、配额列与索引；历史 run 回填 | 结构由 gorm 建（含 `idx_runs_quota_bucket`）；回填是历史数据，不需要 |
| 011_run_workspace_editor | 编辑者列 | 不需要，gorm 已建 |
| 012_assistant_generations | 生成记录表 | 不需要，gorm 已建 |
| 013_workspace_checkpoints | 检查点表 | 不需要，gorm 已建（blob 为 `bytea`） |
| 014_assistant_group_followups | 群聊跟进表 | 不需要：代码已不引用此表（020 起停用，MySQL 上仅为审计保留） |
| 015_sop_handoff_jobs | 交接任务表、`lease_handoff_id` 列 | 表同 014，代码不再引用；`lease_handoff_id` 列 gorm 已建 |
| 016_mr_delivery_merged_at | `merged_at` 列与索引 | 不需要，gorm 已建 |
| 016_public_optional_repositories | 公共 SOP 提示词数据修补 | 不需要，seed 已是修补后的内容 |
| 017_bot_sop_controls | `sop_handoff_jobs` 新列 | 不需要，同 015 |
| 018_requirement_spaces | 空间相关表、`thread_id` 列 | 不需要，gorm 已建 |
| 018_workspace_restore_status | 恢复状态列 | 不需要，gorm 已建 |
| 019_page_feedback | 页面反馈两张表 | 不需要，gorm 已建 |
| 020_conversation_workspaces | `runs.kind` 列；旧群聊 run 转换与取消 | 列由 gorm 建；其余是历史数据，不需要 |
| 021_assistant_session_sources | `source`、`task_id` 列与索引；回填 | 结构由 gorm 建；回填是历史数据，不需要 |
| 022_auth_integrations | 表 | 不需要，gorm 已建 |
| 023_role_prompt | 公共目录 payload 整体替换 | 不需要，seed 已是替换后的内容 |
| 024_native_sandbox_driver | seatbelt/bwrap 改为 native | 不需要：历史数据；seed 里只有 `local` 和 `docker` |
| 024_platform_codebuddy_key | 表 | 不需要，gorm 已建 |
| 025_agent_cli_credentials | 列 | 不需要，gorm 已建 |
| 026_assistant_session_reads | 列与表 | 不需要，gorm 已建 |
| 027_cli_model_catalog | 列 | 不需要，gorm 已建 |
| 028_git_ssh_credentials | 表 | 不需要，gorm 已建 |
| 029_assistant_diagnostics | 表 | 不需要，gorm 已建 |
| 030_generic_docker_shared | `generic-docker` 标记为 shared | 不需要，seed 已带 `"shared":true` |
| 031_generic_backend_openssh | Dockerfile 加 openssh-client | 不需要，seed 已是 034 之后的内容 |
| 032_assistant_generation_workspace_lease | 租约列 | 不需要，gorm 已建 |
| 033_generic_sop_mr_node | 代码 SOP 增加「发起 MR」节点 | 不需要，seed 已含 MR 节点（`TestPublicCodeSOPsOpenMRBeforeDeliver` 校验 MySQL seed，一致性测试保证 PG seed 相同） |
| 034_generic_backend_pip | Dockerfile 加 python3-pip、python3-venv | 不需要，seed 已含（`TestPublicTestRolesFailWithoutRunningTests` 断言等于 `runtime.IsolatedDockerfile`） |
| 035_generic_test_role_requires_tests | 测试角色新 prompt | 不需要，seed 已含新 prompt |

MySQL 脚本里有些列是 `NOT NULL DEFAULT ''` / `DEFAULT 0`（如 005 的 `source_id`、018 的 `thread_id`、011 的 `workspace_editor_id`），gorm 在 PG 上建成可空、无默认值。MySQL 全新安装同样只执行 001 和 seed（见 `scripts/install-schema-extensions.sh`），这些列在 MySQL 新库上也是 gorm 的定义，所以 PG 不需要补。

## 大小写不敏感的唯一索引

MySQL 的 `utf8mb4_unicode_ci` 让 `uq_users_login`、`uq_business_domains_name` 不区分大小写。PG 上的等价索引 `idx_users_login_name_lower`、`idx_business_domains_name_lower`（`lower(...)` 唯一表达式索引）由 `repo.Open` 在 PG 分支用增量 DDL 自动创建，不放在本目录：正确性不依赖人工执行 SQL，测试走 `OpenTest` 也会建上。

## 已知限制

- 生成列表达式只用 `->`、`->>`，payload 是 json 或 jsonb 都能建成。但生成列会固定 `payload` 列的类型：对已经执行过 baseline 的库改 `run_telemetry.payload` 或 `run_logs.payload` 的类型（例如 `json` 改 `jsonb`），需要先删掉 `adapter`、`level` 列，改完再执行一次 baseline。gorm 的增量 DDL 只加不改，不会自己触发这个问题。
- 排序规则（collation）沿用建库时的设置，中文排序会和 MySQL 不同。
