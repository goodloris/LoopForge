# Compose 预览环境（Preview Environment）

状态：实施中。基于 2026-11 在一台内网预览主机 + merchant-hub 上的 PoC（全部链路已实测通过）。
与 `frontend-preview-delivery-lifecycle.md` 的关系：该文档的静态预览不变；本方案替代其中 Phase 2/3「服务型 / 全栈预览」的单机实现路线：不做 runner 隧道、不推 registry，由一台专用预览主机直接对外服务。

## 1. 目标与非目标

目标：SOP 在人工验收前，把「需求 × 仓库」的工作分支部署成一套隔离的 `docker compose` 项目，审批人在浏览器里打开真实运行的应用；需求的 MR 合并/关闭或需求结束时自动销毁。

非目标（本期不做）：闲置自动停机（只记录最后访问时间）、HTTPS/正式域名、多台预览主机调度、registry 镜像归档、预览截图。

## 2. 总体结构

```text
浏览器 ──> Caddy (预览主机, :8088) ──forward_auth──> runner 内置 verify 监听
                │
                └─ reverse_proxy lf-<envId>:<port>   (每个预览一个 internal 网络 lf-edge-<envId>，只有 Caddy 同时接入)

控制面 ── runner task: preview_deploy / preview_teardown ──> 预览主机上的 runner
       └─ POST /api/preview-envs/:id/ticket  签发一次性票据 → 跳转 URL
```

- 预览主机 = 一台注册到控制面的普通 runner，控制面用 `AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID` 指定它。
- 鉴权不回调控制面：控制面与 runner 共享 HMAC 密钥，runner 本地校验票据与 cookie（避免每个静态资源请求都跨网回调）。

## 3. 标识与 URL

- `envId = "pe-" + hex(sha256(issueID + "\x00" + repositoryID))[:10]`：同一需求同一仓库重跑 URL 不变。无 issue 的 run 用 runID 代替 issueID。
- URL：`AGENTFLOW_COMPOSE_PREVIEW_BASE_URL` 模板中的 `{id}` 替换为 envId，例如 `http://{id}.192-0-2-10.sslip.io:8088`（sslip 必须用短横线 IP，点分写法会被误解析）。
- compose 项目名 `lf-<envId>`；Caddy 路由文件 `<root>/caddy/routes/<envId>.caddy`。

## 4. 仓库约定（`.loopforge/preview/compose.yaml`）

参考 merchant-hub MR !18。

- 顶层 `x-loopforge-preview: {service: <入口服务>, port: <容器端口>}`，必填。
- 平台注入 `LF_PREVIEW_ID`、`PREVIEW_ADMIN_PASSWORD` 两个环境变量（compose 插值）。
- 校验拒绝（`docker compose config --format json` 的结果上检查）：`ports`、`privileged`、`cap_add`、`devices`、`container_name`、`userns_mode`、`cgroup_parent`、`network_mode`/`pid`/`ipc`/`uts` 为 host 或 `container:`、非 `volume` 类型挂载、`external` 网络、`external` 或带 `driver_opts` 的卷。任何一项违规都不启动。
- 平台生成覆盖文件（每个服务）：`mem_limit: 2g`、`cpus: 2`、`pids_limit: 512`、`security_opt: [no-new-privileges:true]`；`default` 网络 `internal: true`；只有入口服务额外接入 `lf-edge-<envId>`（external）并带别名 `lf-<envId>`。
- 已知限制：`internal` 网络 = 运行期无出站。需要出站的应用后续再提供 egress 策略。

## 5. 鉴权

共享密钥：控制面 `AGENTFLOW_COMPOSE_PREVIEW_SECRET`，runner `LOOPFORGE_PREVIEW_SECRET`，同值，≥32 字节。

- 票据 ticket：`b64url(envId "|" expUnix "|" nonce16B) "." b64url(HMAC-SHA256(secret, "ticket|" + payload))`，有效期 120 秒，一次性（verifier 记 nonce 至过期）。
- Cookie `lf_pv`：`b64url(envId "|" expUnix) "." b64url(HMAC-SHA256(secret, "cookie|" + payload))`，8 小时，`Path=/; HttpOnly; SameSite=Lax`，host-only（不同预览子域互不通用）。
- verify（runner 监听 `LOOPFORGE_PREVIEW_AUTH_ADDR`，空则不启动；Caddy 调用 `GET /verify?env=<envId>`，带 `X-Forwarded-Uri`）：
  1. cookie 有效且 envId 匹配 → 200，记录最后访问时间；
  2. 否则 `X-Forwarded-Uri` 里有 `__lf=<ticket>` 且票据有效、envId 匹配、未用过 → 302 到去掉 `__lf` 的 URI + Set-Cookie；
  3. 其余 → 401。
- 签发：`POST /api/preview-envs/:id/ticket`，要求调用者对该环境所属项目有读权限，返回 `{url}`（`<baseUrl>/?__lf=<ticket>`）。
- 应用自身登录（如 merchant-hub）在网关之后，互不影响。管理员密码由控制面为每个环境随机生成，随部署注入，对有读权限的用户可见。
  `ponytail:` 密码明文存库，仅限 sandbox 预览环境；接入凭据加密存储后再迁移。

## 6. Runner 动作

域常量：`RunnerTaskActionPreviewDeploy = "preview_deploy"`、`RunnerTaskActionPreviewTeardown = "preview_teardown"`，新增 `RunnerVersionComposePreview`（下一个 runner 版本），旧 runner 被调度时给出可读的升级提示。

`RunnerTask.Preview *RunnerPreviewSpec`：`EnvID`、`ComposeFile`（相对仓库根，默认 `.loopforge/preview/compose.yaml`）、`Host`（URL 的 host，不含端口）、`Env map[string]string`（注入的插值变量）、`Ref`（分支）、`SHA`。仓库与 git 凭据复用 `Invocation.Repositories` / `GitHTTPUserID` / `GitSSHUserID`，与 `align_baseline` 相同。

runner 配置：`LOOPFORGE_PREVIEW_ROOT`（默认 `/srv/lfpreview`）、`LOOPFORGE_PREVIEW_CADDY`（Caddy 容器名，默认 `lf-caddy`）、`LOOPFORGE_PREVIEW_AUTH_ADDR`、`LOOPFORGE_PREVIEW_SECRET`、`LOOPFORGE_PREVIEW_AUTH_UPSTREAM`（写进 Caddy 路由的 verify 地址，默认 `host.docker.internal:9001`）。

preview_deploy：
1. 同一 envId 已是 ready 且 SHA 相同 → 直接成功（no-op）。
2. 用下发的凭据把仓库检出到 `<root>/previews/<envId>/src` 的指定 SHA（浅克隆或 fetch 该 SHA）。
3. 校验（§4）→ 写覆盖文件 → 确保 `lf-edge-<envId>`（`--internal`，标签 `lf.preview=<envId>`）存在且 Caddy 已接入。
4. `docker compose -p lf-<envId> -f <compose> -f <override> up -d --build --remove-orphans --wait --wait-timeout 600`，`DOCKER_BUILDKIT=1`。
5. 写 Caddy 路由并 `caddy reload`；路由内容：`http://<host> { forward_auth <authUpstream> { uri /verify?env=<envId> } reverse_proxy lf-<envId>:<port> }`。
6. 结果 JSON 写入 `Result`：`{status, sha, seconds, logsTail}`；失败时 `Error` 带校验错误或 compose 输出末尾，并附 `docker compose logs --tail 80`。

preview_teardown：`down -v --remove-orphans --rmi local`、删路由并 reload、断开并删除 edge 网络、删目录；结果报告按标签统计的残留容器/卷/网络数（应为 0）。幂等：环境不存在也算成功。

预览主机一次性安装的 Caddy 边缘栈放在 `deploy/preview-edge/`（compose + Caddyfile + README），Caddy 需 `extra_hosts: host.docker.internal:host-gateway`，`admin localhost:2019`，`auto_https off`，未知 Host 返回 404。

## 7. 控制面

- 配置：`AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID`、`AGENTFLOW_COMPOSE_PREVIEW_BASE_URL`、`AGENTFLOW_COMPOSE_PREVIEW_SECRET`。缺任一项则功能关闭，compose 预览节点直接失败并说明缺什么。
- 数据：`PreviewsRepo` 新增 `preview_environments` 表：`id`(envId)、`project_id`、`issue_id`、`repository_id`、`runner_id`、`last_run_id`、`branch`、`sha`、`status`(deploying/ready/failed/destroying/destroyed)、`url`、`admin_password`、`last_error`、`deployed_at`、`destroyed_at`、`created_at`、`updated_at`。按仓库约定补 `migrations/NNN_*.sql`。
- SOP 接入：`PreviewContract.Kind` 新增 `compose`（可选 `composeFile`）。启用后，复用现有合成预览节点的插入位置，节点执行改为：对 run 绑定的每个含 compose 文件的仓库，确保工作分支当前提交已推到远端，然后派发 `preview_deploy` 并等待；全部成功 → 节点成功并把 URL 写入节点输出；任一失败 → 节点失败（可重试）。仓库里没有 compose 文件 → 跳过该仓库并在日志说明。
- 销毁触发：需求 MR 合并或关闭（`tracker/mr_delivery_sync.go` 观察到状态变化时）、需求转为 done/cancelled/closed、手动 `DELETE /api/preview-envs/:id`。销毁走 `preview_teardown`，成功后标记 destroyed。
- API（写进 OpenAPI，handler 新建 `previewEnvHandlers`）：`GET /api/runs/:id/preview-envs`、`GET /api/issues/:id/preview-envs`、`POST /api/preview-envs/:id/ticket`、`DELETE /api/preview-envs/:id`。
- Studio：run 详情与需求详情里显示预览环境卡片（状态、SHA、管理员账号密码、「打开预览」按钮 → ticket → 新标签页打开）。

## 8. 实测数据（PoC）

merchant-hub：冷构建约 7 分钟，改前端后约 3.5 分钟，无改动重部署约 30 秒，同 SHA no-op 即时；空闲内存约 9 MB/容器。老内核（3.10）上 alpine + pnpm 11 的 SQLite 写入被默认 seccomp 拒绝，仓库 Dockerfile 需用 glibc 基础镜像。
