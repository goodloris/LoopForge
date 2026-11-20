# 参与贡献

平台代码位于仓库根目录，原本地工作流版本位于 `local/`。开始修改前请阅读对应的 `AGENTS.md`。

平台改动应保留上游 engine、repo 与 handler 的分层；身份、外部提供商和部署差异应通过明确适配实现。修复行为问题时附带回归测试，说明 schema 与 runner 协议兼容性。

```bash
go test -p=2 ./... -count=1 -timeout=180s
go vet ./...
python3 scripts/check-public.py
bash scripts/scan-secrets.sh
```

`.loopforge/verify` 是仓库的测试门禁。在仓库根目录执行 `.loopforge/verify`（或 `bash .loopforge/verify`）即可运行。需要 Go（`go.mod` 指定版本）、Node 22+ 和能访问 npm registry 的 npm，不需要数据库、Docker 或凭证。它按顺序执行，遇到第一个失败即停止：前端构建并跑 chatbot 测试（`scripts/build-chatbot.sh`）、`go build ./...`、`go vet ./...`、`go test ./... -count=1`（按 CI 拆分，真实浏览器测试和 fake docker 测试在独立进程中运行）、`webtest` 单元测试。每步输出 `==> [n/5]` 和耗时；失败时输出 `FAILED: [n/5] <步骤名>` 并以非 0 退出。可用 `GO_TEST_P`（默认 2）、`GO_TEST_TIMEOUT`（600s）、`GO_BROWSER_TEST_TIMEOUT`（300s）和 `GOCACHE`（默认 `/tmp/agentflow-gocache`）调整。

在 SOP 流程中，「测试验证」节点提交 pass 后，平台会运行目标分支上已提交的 `.loopforge/verify`：先复制到临时文件，再以仓库根为工作目录、关闭 stdin 执行。非 0 退出码会把输出打回「开发实现」。修改脚本要等合入目标分支后才生效，所以脚本必须以当前目录为仓库根，不能依赖自身路径。如需排除测试，必须在脚本注释中逐条写明原因。

社区贡献不需要私有来源清单，也不需要更新文件哈希。维护者在本地保存来源与同步审计记录，公共 CI 不依赖这些资料。长期流程见[维护策略](docs/upstream-sync.md)。

登录、语言/主题或执行链路的改动需要跑[公共浏览器验收](e2e/public/README.md)。真实模型与外部知识库测试需显式配置；请区分通过、失败、跳过与未运行。

每次发布前，以及更新 kiro-cli 固定版本时（`internal/runtime/kiro_login.go` 的 `KiroCLIVersion` 和 `Dockerfile`），都要手动运行一次 Kiro CLI 真机审批测试。CI 不跑这项测试，因为它需要本机已执行 `kiro-cli login`，还会消耗少量 Kiro credits。它验证三件事：等待批准超过 Kiro 默认的 60 秒 hook 超时后，shell 命令仍被阻塞；被拒绝的命令不执行；批准后的命令会执行。

```bash
LOOPFORGE_KIRO_E2E=1 go test ./internal/runtime/ -run TestKiroConversationApprovalHookRealCLI -count=1 -v -timeout 15m
```

平台的 pull request 会跑 `core`、`browser` 和 `first-run` 三个作业。稳定检查名 `platform-required` 只有三者都成功时才通过。仓库管理员需要在 GitHub 分支保护里把 `platform-required` 手动勾成必须通过的检查。这次代码改动不会修改 GitHub 上的分支保护设置。

LoopForge Local 请在 `local/` 内修改，遵循[本地开发约定](local/AGENTS.md)。先明确改动属于 Classic、Portable 或共享行为合同；三者与平台运行资产保持清晰边界。Portable 修改 `local/skills/`；Classic 的 Cursor/Claude 包在 `local/` 内运行 `python3 scripts/build-classic-hosts.py --write` 生成。工作流模板保持通用，可选工具放在 `local/tools/`，单独分发。

在 `local/` 内执行本地版检查：

```bash
python3 scripts/build-classic-hosts.py --check
bash scripts/validate.sh
bash scripts/smoke-install.sh
bash scripts/smoke-npm.sh
bash scripts/smoke-curl-install.sh
bash scripts/e2e.sh
bash scripts/test-observability.sh
bash scripts/scan-secrets.sh
```

[本地 E2E](local/tests/e2e/README.md) 使用模拟宿主与录制产物，无需模型凭据。分别说明对各工作流的影响，保留既有权限语义和用户文件保护；提交信息遵循 Conventional Commits。

PR 不应包含凭据、内部基础设施、员工权限默认值或生产数据。保留用户文件和第三方许可，并记录贡献来源。PR 说明应写清问题、最终行为和实际验证结果。
