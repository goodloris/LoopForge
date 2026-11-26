# Compose 预览环境边缘栈（preview-edge）

预览主机上一次性安装的 Caddy。runner 执行 `preview_deploy` 时把每个预览的路由写到
`$LOOPFORGE_PREVIEW_ROOT/caddy/routes/<envId>.caddy` 并 `caddy reload`；浏览器请求先经
`forward_auth` 交给 runner 内置的 verify 监听校验票据/cookie，再反代到该预览的入口服务。
设计见 `docs/proposals/compose-preview-environments.md`。

## 安装

前提：Linux 主机，Docker Engine + Compose 插件，已按「执行机器」页面安装并注册 runner（0.12.0+）。

```bash
sudo mkdir -p /srv/lfpreview/caddy/routes          # 与 LOOPFORGE_PREVIEW_ROOT 一致
sudo chown -R "$(id -u)" /srv/lfpreview             # runner 用户要能写 routes 与 previews
cd deploy/preview-edge
docker compose up -d                                # 默认对外 8088，LF_PREVIEW_EDGE_PORT=80 可改
curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: nope' http://127.0.0.1:8088/   # 404 = 正常
```

`LOOPFORGE_PREVIEW_ROOT` 不是默认值时，启动边缘栈也要带上同一个变量。

## runner 环境变量

写进 runner 的 `.env`（与 `AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN` 同处）或 systemd 环境：

```bash
LOOPFORGE_PREVIEW_ROOT=/srv/lfpreview                 # 预览目录与 Caddy 路由目录的根，默认 /srv/lfpreview
LOOPFORGE_PREVIEW_CADDY=lf-caddy                      # Caddy 容器名，默认 lf-caddy
LOOPFORGE_PREVIEW_AUTH_ADDR=172.17.0.1:9001           # verify 监听地址；为空不启动
LOOPFORGE_PREVIEW_SECRET=<与控制面 AGENTFLOW_COMPOSE_PREVIEW_SECRET 相同，≥32 字节>
LOOPFORGE_PREVIEW_AUTH_UPSTREAM=host.docker.internal:9001   # 写进路由的 verify 地址，默认即此值
```

生成密钥：`openssl rand -hex 32`。密钥不足 32 字节时 verify 监听不启动并打日志，所有预览都会 401。

控制面需配置 `AGENTFLOW_COMPOSE_PREVIEW_RUNNER_ID`（这台 runner 的 ID）、
`AGENTFLOW_COMPOSE_PREVIEW_BASE_URL`（如 `http://{id}.192-0-2-10.sslip.io:8088`，sslip 用短横线 IP）
和同一个密钥。

## 安全边界

- verify 监听应绑在 docker0 网关地址（`ip -4 addr show docker0`，通常 `172.17.0.1`），不要绑
  `0.0.0.0`：它只该被本机的 Caddy 容器访问。若主机防火墙默认拒绝 INPUT，放行来自 Docker 网段到该端口的流量。
  `host.docker.internal:host-gateway` 解析到 docker0 网关，Caddy 在自己的 compose 网络里也能经主机路由访问它。
- 每个预览有自己的 `internal` 网络 `lf-edge-<envId>`，只有 Caddy 同时接入，预览之间互不可达；
  仓库自带的所有网络也被覆盖为 `internal`，运行期没有出站（构建期仍可联网）。
- runner 拒绝：`ports`、`privileged`、`cap_add`、`devices`、`container_name`、`userns_mode`、
  `cgroup_parent`、`security_opt`、host/`container:` 命名空间、非 named volume 挂载、external 网络/卷、
  带 `driver_opts` 的卷，以及指向仓库外的 `env_file`、configs/secrets 文件、构建上下文和 Dockerfile。
- compose 进程只拿到 `PATH`/`HOME`/`DOCKER_*` 等 docker CLI 必需变量和平台注入的
  `LF_PREVIEW_ID`、`PREVIEW_ADMIN_PASSWORD`，runner 自己的令牌与密钥不会进入 `${VAR}` 插值。
- runner 能调用 Docker 即等同主机 root：预览主机应专用，不要与生产或个人开发机混用。
- 入口是 HTTP（本期不做 HTTPS）。cookie 为 host-only，不同预览子域互不通用。

## 已知坑

- 老内核（如 3.10）上 alpine 镜像里 pnpm 写 SQLite 会被默认 seccomp 拒绝：仓库的预览 Dockerfile
  用 glibc 基础镜像（如 `node:22-bookworm-slim`）。
- `internal` 网络下应用运行期不能访问外网（第三方 API、对象存储等），需要的话等后续 egress 策略。
- `teardown` 用 `--rmi local` 只删没有自定义 tag 的镜像；compose 里写了 `image: xxx:${LF_PREVIEW_ID}`
  的构建镜像会留下，定期 `docker image prune` 或按 tag 清理。
- 改了 `LOOPFORGE_PREVIEW_AUTH_UPSTREAM` 后，已有路由要重新部署才会更新。
