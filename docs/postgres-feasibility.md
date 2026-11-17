# PostgreSQL 支持可行性（原型结论）

分支 `proto/postgres`，基于 `origin/main` 5009086。这是一天的验证原型，目的是拿到真实失败清单来校准估算，不是完整改造。

## 结论

- gorm 的 AutoMigrate 和生产 `Open()` 路径在 PG 17 上能跑通：空库建出全部 77 张表，第二次 `Open()` 不再做任何 DDL。
- 测试接入 PG 后，`internal/repo` 和 `internal/engine` 合计 **778 通过 / 18 失败 / 29 跳过**（repo 304/16/29，engine 474/2）。29 个跳过的是只在 MySQL 上跑的集成测试，sqlite 下同样跳过。
- 剩下 18 个失败里，11 个是产品代码缺 PG 分支（6 类，写法都已明确），7 个只是测试本身绑定了 sqlite（SQL 文本断言、非法 JSON 夹具、sqlite 触发器语法）。
- 不设 `AGENTFLOW_TEST_PG_DSN` 时 sqlite 路径不受影响：`go test ./internal/repo/... ./internal/engine/... -count=1` 795 通过 / 30 跳过 / 0 失败。
- 修正后的估算：**约 13–19 人日**（明细见文末）。主要工作量不在 SQL 翻译，而在：把 29 个 MySQL 专属并发测试移植到 PG、安装与 compose 切换默认 PG、PG 基线和 seed。

## 原型做了什么

### 方言选择（`internal/repo/repo.go`）

- `deps` 新增 `dialect()`（直接返回 gorm 的 `Dialector.Name()`：`mysql` / `postgres` / `sqlite`）和 `isPostgres()`。`isMySQL()` 行为不变。
- `Open(dsn)` 按 DSN 选方言：`postgres://`、`postgresql://` 开头走 `gorm.io/driver/postgres`（pgx v5），其他按 go-sql-driver/mysql DSN 处理，MySQL 的 charset/parseTime 补全逻辑原样保留。PG 连接若未指定会补 `timezone=UTC`。
- 新环境变量提议 **`AGENTFLOW_DATABASE_DSN`**。查找顺序：显式参数（`-mysql-dsn` flag） > `AGENTFLOW_DATABASE_DSN` > `AGENTFLOW_MYSQL_DSN`（兼容旧部署）。
- 限制：libpq 的 key/value 形式（`host=... dbname=...`）不识别，会被当成 MySQL DSN。只支持 URL 形式。
- 新增依赖：`gorm.io/driver/postgres v1.6.3`，间接带入 `jackc/pgx/v5`、`puddle`、`pgpassfile`、`pgservicefile`、`golang.org/x/sync`。

### 建表阻断项：blob 类型

`type:mediumblob` / `type:longblob` 在 PG 上建表直接失败。两种写法：

| 写法 | 优点 | 缺点 |
|---|---|---|
| 去掉 `type:`，用 `size` 让 gorm 按方言映射（**已采用**） | 一个 tag 管三种方言；MySQL 新建出的类型和以前完全一样 | 依赖 gorm mysql 驱动的映射规则（`size` 在 65536..2^24 → mediumblob，无 size → longblob） |
| 按方言在 `autoMigrate` 里改类型 / 自定义 `GormDBDataType` | 显式 | 每个字段都要写分支或类型，代码多 |

改动：4 个 mediumblob 字段改为 `size:16777215`，2 个 longblob 字段去掉 type（MySQL 默认即 longblob）。PG 上都是 `bytea`，sqlite 上是 `blob`。`autoMigrateIncremental` 只补缺失对象，不会去改存量 MySQL 列。`blob_types_test.go` 不连库，直接断言 MySQL/PG 两边的类型，防止以后有人改回去。

`type:json` 在 PG 上建成 `json`（保留原文的文本类型），能直接用；是否换 `jsonb` 见“后续建议”。

### 测试接入（`internal/repo/opentest.go`）

- 设置 `AGENTFLOW_TEST_PG_DSN=postgres://user:pass@host:port/db?sslmode=disable` 后，`OpenTest()` 在该库里为每次调用新建 schema `lf_t_<pid>_<seq>`，通过连接参数 `search_path` 绑定，然后照常 AutoMigrate + seed。不同包的测试进程并行跑也互不干扰。
- 每个测试 store 最多 4 个连接（真实行锁和隔离级别能生效），空闲 2 秒回收，防止没 Close 的测试 store 把 `max_connections` 耗尽。
- 每个测试 schema 约 0.2 秒建好，repo+engine 全量约 2 分钟（sqlite 约 45 秒）。
- 不设环境变量时代码路径和以前一致（单连接内存 sqlite）。
- 已知限制：schema 不会自动删除（`Store.Close` 没有测试钩子）。一次 repo+engine 全量会留下约 2.8 GB 数据。测试实例请用 `--tmpfs /var/lib/postgresql/data`，并加 `--shm-size=1g`。默认 64 MB 的 `/dev/shm` 在并发查询下会耗尽，PG 后端会 SIGSEGV 重启，这次原型遇到过一次。

本地复现：

```bash
docker run -d --name lf-proto-pg --tmpfs /var/lib/postgresql/data:size=6g --shm-size=1g \
  -e POSTGRES_PASSWORD=proto -p 127.0.0.1:55433:5432 postgres:17-alpine -c max_connections=3000
AGENTFLOW_TEST_PG_DSN='postgres://postgres:proto@127.0.0.1:55433/postgres?sslmode=disable' \
  go test ./internal/repo/... ./internal/engine/... -count=1
docker rm -f -v lf-proto-pg
```

## 失败按根因分类

失败数随修复推进的变化（repo 包，单位：顶层测试）：

| 阶段 | repo 失败数 |
|---|---|
| 只接入 PG、修好 blob | 全部 OpenTest 用例失败在 seed（`ON CONFLICT DO UPDATE requires inference specification`） |
| 修 upsert 冲突目标 | 60 |
| + insertIgnore 方言、users 序列、GET_LOCK、DB 时钟 | 52 |
| + Run 列表 JSON 投影 | 45 |
| + 会话元数据 JSON_REMOVE | 22（另有一次 PG 因 shm 崩溃导致的假失败已排除） |
| + RunView 节点 JSON_SET 投影 | 16 |

engine 包：首轮 3 个失败，复用 `sqlPayloadJSONText` 修掉 1 个，剩 2 个都是测试夹具。

### 已修复（每类至少一个示范）

| # | 根因 | 影响 | 位置 | PG 写法 |
|---|---|---|---|---|
| F1 | `upsert` 用 `Table(name)` + map 写入，gorm 拿不到主键；sqlite/MySQL 允许不写冲突目标，PG 不允许 | 所有测试（seed 即失败），以及生产上所有 payload 表写入 | `payload.go` `upsert`/`upsertColumns` | 新增 `conflictTarget()`：只在 PG 上从 `allModels()` 解析主键作为 `ON CONFLICT (pk)`。其他方言保持“任意唯一键冲突”的语义不变 |
| F2 | `insertIgnore` 只有 `INSERT IGNORE` / `INSERT OR IGNORE`，且列名用反引号 | 用户创建 | `payload.go` | `INSERT ... ON CONFLICT DO NOTHING`，列名用双引号 |
| F3 | 显式写入的 id 不会推进 PG 序列（MySQL AUTO_INCREMENT 会） | 8 个测试，表现为 `record not found`：seed 写了 id=1 的用户，下一个不带 id 的插入拿到 1，冲突后被 DO NOTHING 吞掉 | `users.go` `writeUser` | 显式 id 写入后 `setval(pg_get_serial_sequence('users','id'), max(id))` |
| F4 | `GET_LOCK`/`RELEASE_LOCK` | 合入锁（生产路径，sqlite 测试走进程内 mutex，所以没有测试失败） | `integration_lock.go` | 专用连接上 `SET lock_timeout` + `pg_advisory_lock(hashtext(name))`，超时 SQLSTATE 55P03 转成原来的超时错误。新增 `integration_lock_pg_test.go`（仅 PG 下运行） |
| F5 | `UTC_TIMESTAMP(3)`、`UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND` | 助手心跳时钟、回收判断 | `assistant_generations.go` | `statement_timestamp()`、`statement_timestamp() - ? * interval '1 microsecond'`。不用 `now()`，因为它冻结在事务开始时间 |
| F6 | `locking()` 只在 MySQL 加 `FOR UPDATE` | 不报错，但 PG 上租约/准入/投递的行锁全部失效。测试是单进程，看不出来 | `repo.go` | 改为 MySQL 或 PG 都加 `FOR UPDATE` |
| F7 | `sqlJSONText` 等三个 JSON 取值辅助函数没有 PG 分支，回落到 sqlite 的 `json_extract` | 所有用到辅助函数的筛选 | `repo.go` | `(jsonb_path_query_first(col::jsonb, '$.a.b'::jsonpath) #>> '{}')`，与 MySQL 共用同一个 `$.` 路径常量 |
| F8 | 手写的 `JSON_OBJECT`/`json_object` 投影 | 7 个 Run 列表、分析测试 | `runs.go` 列表 snapshot、`runNodeSummaries`、`LiveSandboxDockerfiles` | `json_build_object('k', col->>'k')`；`->` 保留 JSON 值，`->>` 去引号 |
| F9 | `JSON_REMOVE(payload,'$.messages')` | 23 个：分析快照遇到任何错误会整体返回 0，一个会话元数据查询失败就让大部分分析测试归零 | `assistant_shared.go` `assistantSessionMetadataColumns`（`analytics_activity.go` 也用它） | `(payload::jsonb - 'messages')` |
| F10 | `JSON_SET(... JSON_TYPE ...)` 截断大字段 | 6 个 RunView / 日志 / 流视图测试 | `runs.go` `runViewNodesQuery` | `payload::jsonb \|\| jsonb_build_object('output', CASE WHEN json_typeof(...)='string' THEN to_jsonb(SUBSTR(...)) ELSE (...)::jsonb END, ...)`。注意 `::` 优先级高于 `->`，必须加括号 |
| F11 | `TelemetryEventExists` 重复实现了 JSON 取值 | 1 个 engine 测试（遥测幂等） | `runs.go` | 改用 `sqlPayloadJSONText("id")`，三方言统一 |

### 未修复：产品代码缺 PG 分支（11 个测试）

| # | 根因 | 数量 | 代表测试 / 报错 | 位置 | PG 写法 | 预估 |
|---|---|---|---|---|---|---|
| U1 | 会话摘要投影：`JSON_SET(JSON_REMOVE(...))` + `JSON_TABLE` 参与者汇总；回填用 `JSON_SET` | 6 | `TestAssistantSessionListsDoNotTransferTranscripts`：`function json_remove(json, unknown) does not exist`；`TestAssistantParticipantBackfillForLegacyRows`：`function json_set(...) does not exist` | `assistant.go` `assistantSessionSummaries`、`assistant_context.go` `assistantParticipantSummarySQL`（JSON_TABLE）、`assistant.go` 回填 | `jsonb_set(payload::jsonb - 'messages', '{executionMessageCount}', ...)`；JSON_TABLE 换成 `jsonb_to_recordset(payload::jsonb->'messages') AS m(role text, "authorId" bigint, ...)`；`json_array_length` | 1 天 |
| U2 | 每条消息的 usage 聚合：MySQL 用 `JSON_TABLE`+`JSON_ARRAYAGG`，sqlite 用 `json_each`+`json_group_array` | 2 | `TestAnalyticsActivityTokenUsageUsesReplyTimeAndExcludesInheritedHistory`：`function json_each(json, unknown) does not exist` | `analytics_activity_usage.go` | `json_array_elements(payload->'messages') WITH ORDINALITY m(value, pos)` + `json_agg(json_build_object(...))` | 0.5 天 |
| U3 | 工具成功率：`json_extract` 当布尔/数字求和 | 1 | `TestAnalyticsBuildsLoopDemoMetricsAndTraceChain`：`ToolCallsObserved:0` | `analytics.go` 约 683 行 | `CASE WHEN payload#>>'{tool,success}' IS NOT NULL THEN (payload#>>'{tool,success}')::boolean::int ...`；注意 JSON `null` 和 SQL `NULL` 的区别 | 0.25 天 |
| U4 | `INSTR(CAST(payload AS TEXT), ?)`（sqlite 分支） | 1 | `TestReplaceInstalledCLIFailureKeepsConcurrentEdits`：`function instr(text, unknown) does not exist` | `assistant_cli_notice.go` | `strpos(payload::text, ?) > 0` | 0.1 天 |
| U5 | **静默错误**：`CAST(payload AS CHAR) LIKE ?`，在 PG 上 `CHAR` 等于 `char(1)`，只保留第一个字符，查询不报错但永远匹配不到 | 1 | `TestRoomViewsFiltersSignalsAndFallbacks`：attachment message 为空 | `rooms.go` `MessageWithAttachment` | `payload::text LIKE ?` | 0.1 天 |
| U6 | 不报错但走了 Go 侧兜底的分支（结果正确、性能差）：日志/遥测分页把整个 run 的日志读进内存、分析 coverage 在 Go 里算、通知出队没用 `SKIP LOCKED`（后面的 `status`+`attempts` 条件更新保证不会重复发送，但多个 worker 会争抢同一行，输的一方空转一轮） | 0（测试通过） | — | `runs.go` `QueryRunLogs`/`QueryRunTelemetry`、`analytics.go` coverage、`notifications.go` 135 行 | PG 有对应写法，另需 001 的生成列（见迁移方案） | 1 天 |

U1–U5 合计约 2 天。U6 约 1 天，生产数据量下必须做。

### 未修复：只是测试本身绑定了 sqlite（7 个测试）

| 根因 | 数量 | 测试 | 处理 |
|---|---|---|---|
| 断言 sqlite 的 SQL 文本 | 3 | `TestSQLPayloadJSONTextSQLite`、`TestSQLExecutionContextJSONText`（期望 `json_extract(...)`），`TestQuerySummaryProjectionAndScope`（期望查询含 `JSON_OBJECT`） | 按方言给期望值，或改成断言行为 |
| 夹具往 json 列写非法 JSON 来制造失败（sqlite 不校验，PG 在写入时就拒绝，MySQL 也会拒绝） | 2 | `TestNotificationOutboxAtomicAndIdempotent`、`TestAssistantDiagnosticsReadRefreshesStaleRunningReport`：`invalid input syntax for type json` | 换一种制造失败的方式（例如写合法但不符合结构的 JSON） |
| sqlite 触发器语法 `CREATE TRIGGER ... WHEN NEW.x BEGIN SELECT RAISE(ABORT, ...) END` | 2 | engine `TestSOPNodeArtifactWriteFailureKeepsSuccessfulChild`、`TestUpdateRunErrReportsStoreRejection`：`syntax error at or near "NEW"` | 抽一个测试 helper，PG 上用 `CREATE FUNCTION ... RAISE EXCEPTION` + `CREATE TRIGGER` |

合计 0.5–1 天。

### 偶发

`TestRunLeaseMutualExclusionAndExpiryTakeover` 在 repo+engine 并行跑 PG 时失败过一次（`second acquire error = <nil>`）：测试用 20 ms 租约，负载下两次往返超过 20 ms，租约按设计已过期。单独跑 30 次全部通过，不属于语义问题，但移植到 PG CI 前应该把租约时长调大。

## 语义差异风险

| 主题 | MySQL 现状 | PG 行为 | 对本项目的影响 | 建议 |
|---|---|---|---|---|
| 大小写 | `utf8mb4_unicode_ci`，比较和唯一索引都不区分大小写 | `=`、`LIKE`、唯一索引都区分大小写 | `users.login_name`（`uq_users_login`）在 PG 上允许 `Alice` 和 `alice` 两行；`UserByLoginName` 在非 MySQL 走 `LOWER()=LOWER()`，多行时 `Take` 随机取一行，且用不上索引。`business_domains.name`（`uq_business_domains_name`）同理。代码里另有 11 处 `LIKE`，PG 上全部变成区分大小写 | PG 基线建 `UNIQUE (lower(login_name))`、`UNIQUE (lower(name))` 表达式索引（或用 `citext`），`LIKE` 按需改 `ILIKE`。字符串排序还取决于 PG 的 `LC_COLLATE`，中文排序会和 MySQL 不同，基线里应固定 collation |
| GROUP BY | MySQL 8 默认 `ONLY_FULL_GROUP_BY` | 严格 | 检查了 5 处 GROUP BY（rooms、shared_schedules、run_artifacts 计数、analytics coverage、遥测成功率），都只选分组列和聚合列 | 风险低，无需改动 |
| 空串与 NULL | 区分 | 区分（和 MySQL 一致） | 真正的差别在别处：PG 的 text 拒绝 `0x00`，`jsonb` 还拒绝 `\u0000`，MySQL 都接受。agent 输出、日志里出现 NUL 时写入会报错。varchar 超长时 PG 一律报错 | 写入路径对外部输出做 NUL 清洗；如果改用 jsonb，要先处理 `\u0000` |
| 隔离级别 | 默认 REPEATABLE READ。准入事务已显式降到 READ COMMITTED（`admissionTxOptions`） | 默认 READ COMMITTED，没有间隙锁 | 准入：PG 默认就是 RC，`lockAdmissionGate` 用“先插入占位行再 `FOR UPDATE`”，不依赖间隙锁，行为一致。租约：`takeRunForUpdate` 依赖 `locking()`，原型已让 PG 也加 `FOR UPDATE`（F6）；修复前 PG 上的租约互斥会**静默失效**。`BeginAssistantGeneration` 注释写明依赖 InnoDB 对不存在行的间隙锁，并且只重试 MySQL 1213 死锁；PG 上锁不存在的行什么都锁不到，并发首条消息会表现为唯一键冲突（23505），而不是可重试的死锁 | 把 `retryAssistantDeadlock` 扩展到 PG 的 40P01、40001，必要时也包括 23505；把 29 个 MySQL 专属并发测试移植到 PG，这是验证租约和准入唯一可靠的方法 |
| 自增序列 | 显式 id 会推进 AUTO_INCREMENT | 不推进 | 已修 users（F3）。`business_domain_assignments.id` 也是自增列，需要确认没有显式写 id 的路径 | 基线 seed 写完后对所有 serial 列统一 `setval` |
| 错误码 | 代码里写死 MySQL 错误号：1213（死锁重试）、1064/1305（不支持 JSON_TABLE 时降级）、1366/3988（bot 消息字符集提示） | SQLSTATE | PG 上这些分支永远不会触发 | 改用 `pgconn.PgError.Code` 判断，或在 repo 层统一一个“可重试错误”判断函数 |
| 时间 | `parseTime=true&loc=UTC`，读出的 `time.Time` 是 UTC | `timestamptz`；pgx 读出的 `time.Time` 用进程本地时区（原型日志里出现过 `+0800 CST`），即使连接设了 `timezone=UTC` | `Equal` 比较不受影响；直接把行里的时间序列化成 JSON 会输出 `+08:00` 而不是 `Z` | 容器里设 `TZ=UTC`，或在 row→domain 转换处统一 `.UTC()` |

## migrations/ 方案建议

现状：`migrations/` 有 40 个按序号的 MySQL 脚本和 `seed/` 两个文件。脚本里大量 MySQL 专属语法（统计：`JSON_EXTRACT` 118 处、`JSON_UNQUOTE` 86、`UTC_TIMESTAMP` 71、`JSON_SET` 60、`AS JSON` 37、`INSERT IGNORE` 14、`ENGINE=`/`CHARSET` 26），而且很多是针对历史数据的修补。逐个翻译没有意义：PG 不需要迁移存量数据，只需要“当前状态”。

建议目录：

```
migrations/
  001_...sql … 035_...sql      # 维持原样，仅 MySQL
  seed/                        # 维持原样，仅 MySQL
  postgres/
    000_baseline.sql           # 当前 schema 的 PG 版本
    seed/
      001_public_catalog.sql   # 当前 catalog 状态（已包含 030–035 对 generic 模板的修改）
      002_welcome_project.sql
    001_...sql                 # 以后的 PG 增量脚本，与 MySQL 脚本成对提交
```

`000_baseline.sql` 的内容：

1. gorm 建的表：PG 上 `Open()` 本身就会建出全部表（已验证）。基线可以用“空库跑一次 Open，再 `pg_dump --schema-only`”生成，这样既符合 AGENTS.md“基线 schema 写成 SQL 脚本”的规定，也保证和模型一致。
2. 001 的生成列（PG 12+ 支持 STORED 生成列，`json` 的 `->>` 是 immutable，可以直接用）：
   ```sql
   ALTER TABLE run_telemetry ADD COLUMN adapter varchar(255)
     GENERATED ALWAYS AS (COALESCE(NULLIF(payload->'source'->>'adapter', ''), 'unknown')) STORED;
   CREATE INDEX idx_run_telemetry_run_adapter ON run_telemetry (run_id, adapter);
   ALTER TABLE run_logs ADD COLUMN level varchar(32)
     GENERATED ALWAYS AS (COALESCE(NULLIF(payload->>'level', ''), 'info')) STORED;
   CREATE INDEX idx_run_logs_run_level ON run_logs (run_id, level);
   ```
3. 大小写不敏感的唯一索引：`lower(login_name)`、`lower(business_domains.name)`。
4. 固定 collation、`TimeZone=UTC` 的说明。

seed 的生成方式：在全新 MySQL 上按顺序执行 001–035 + seed，导出 catalog 相关表的行，再转成 `INSERT ... ON CONFLICT DO NOTHING`。不要手工翻译 030–035 里的 `JSON_SET` 修补。seed 写完后对 serial 列执行 `setval`。

防漂移：加一个 CI 用例，在应用了 `postgres/000_baseline.sql` 的空库上调用 `Open()`，断言 `autoMigrateIncremental` 没有执行任何 DDL。

## 需要改的脚本和 compose（只列出，未改）

| 文件 | MySQL 出现次数 | 需要做的 |
|---|---|---|
| `compose.yaml` | 12 | 新增 postgres 服务（镜像、healthcheck、volume、`shm_size`），平台服务改用 `AGENTFLOW_DATABASE_DSN`，MySQL 服务改为可选 profile |
| `start.sh` | 2 | 默认 PG；`logs/stop/restart/pull` 子命令里的服务名 |
| `.env.example` | 2 | `AGENTFLOW_DATABASE_DSN` 示例，保留 MySQL 示例 |
| `scripts/install.sh` | 9 | 生成 PG 密码/DSN、等待 PG 就绪、执行 `migrations/postgres/` 基线和 seed |
| `scripts/configure.sh` | 2 | DSN 生成 |
| `scripts/install-observability-schema.sh` | 4 | PG 版生成列（或并入 PG 基线后跳过） |
| `scripts/install-schema-extensions.sh` | 1 | 同上 |
| `scripts/manual-migration-hints.sh` | 2 | 区分方言，PG 指向 `migrations/postgres/` |
| `scripts/dev.sh` | 3 | 本地开发默认起 PG |
| `scripts/workbench-dev.sh` | 7 | 同上 |
| `scripts/workbench-seed.sh` | 3 | PG seed |
| `scripts/test-container.sh` | 4 | 容器测试改用 PG |
| `scripts/test-install.py` | 13 | 安装测试的断言（服务名、DSN、表检查） |
| `scripts/test-install-live.sh` | 5 | 同上 |
| `cmd/agentflow/main.go` | 1 | `-mysql-dsn` 的说明文字写着“MySQL is the only data source”；建议新增 `-database-dsn`，保留旧 flag 作为别名 |
| `docs/deployment.md`、README（中英） | — | 部署说明 |
| `.github/workflows/*.yml` | 0 | 不直接引用 MySQL；如果要在 CI 跑 PG 测试，需要新增一个 postgres service job，并设置 `AGENTFLOW_TEST_PG_DSN` |

## 修正后的工作量估算

| 项目 | 人日 |
|---|---|
| U1–U5 补齐 PG SQL 分支 + 错误码判断（1213/1064/1305/1366 → SQLSTATE） | 2–3 |
| U6 性能对齐（日志/遥测分页、coverage、`SKIP LOCKED`）+ jsonb/表达式索引评估 | 1–2 |
| 7 个 sqlite 绑定测试改成方言无关 | 0.5–1 |
| 29 个 MySQL 专属集成测试（租约、DB 时钟、准入、工作区租约）移植到 PG 并在 CI 跑通 | 3–4 |
| 语义修正：大小写唯一索引、序列、`BeginAssistantGeneration` 间隙锁依赖、NUL 清洗、时区 | 1–2 |
| `migrations/postgres/` 基线 + seed + 防漂移用例 | 1.5–2 |
| 安装、compose、脚本默认 PG，安装测试改造 | 2–3 |
| `internal/httpapi`（247 处 OpenTest，这次没跑）及其他包在 PG 上跑一遍，修残余问题 | 1–2 |
| **合计** | **约 13–19 人日** |

不确定性主要来自两处：一是 29 个 MySQL 专属测试移植后会暴露多少并发问题；二是 httpapi 包没有在 PG 上跑过。按 repo 包的经验，httpapi 里多数失败应该和上面的类别重叠。

## 后续建议（不在本原型范围内）

- `json` 换 `jsonb`：每次查询的 `::jsonb` 转换会消失，还能建 GIN 或表达式索引。代价是写入时拒绝 `\u0000`，并且会丢失键顺序和重复键。默认改用 PG 之后再评估。
- `pgJSONText` 用的是 `jsonb_path_query_first`，好处是和 MySQL 共用 `$.a.b` 路径常量。热点查询可以改成 `#>> '{a,b}'`，或者基于生成列加索引。
