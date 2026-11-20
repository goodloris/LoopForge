# Contributing

LoopForge has a platform at the repository root and a complementary local workflow edition in `local/`. Read the applicable `AGENTS.md` before changing either.

For platform changes, preserve upstream engine, repository and handler boundaries. Keep identity, provider and deployment adaptations explicit. Add a regression test for a behavioral fix and document schema or runner-protocol compatibility changes.

The platform binary embeds the chatbot frontend from `web/native-chat`, so build it (Node 22+) before the Go checks; this also runs the chatbot tests.

```bash
bash scripts/build-chatbot.sh
go test -p=2 ./... -count=1 -timeout=180s
go vet ./...
python3 scripts/check-public.py
bash scripts/scan-secrets.sh
```

`.loopforge/verify` is the repository's test gate. Run it from the repository root with `.loopforge/verify` (or `bash .loopforge/verify`). It needs Go (the `go.mod` version), Node 22+ and npm with registry access; no database, Docker or credentials. It runs, in order and stopping at the first failure: the frontend build with the chatbot tests (`scripts/build-chatbot.sh`), `go build ./...`, `go vet ./...`, `go test ./... -count=1` (split like CI: real-browser and fake-docker tests run in their own processes), and the `webtest` unit tests. Each step prints `==> [n/5]` and its duration; a failure prints `FAILED: [n/5] <step>` and exits non-zero. Tune it with `GO_TEST_P` (default 2), `GO_TEST_TIMEOUT` (600s), `GO_BROWSER_TEST_TIMEOUT` (300s) and `GOCACHE` (default `/tmp/agentflow-gocache`).

In the SOP workflow, after the "test verification" node submits pass, the platform runs the version of `.loopforge/verify` committed on the target branch: it copies the file to a temporary path and runs it with the repository root as the working directory and stdin closed. A non-zero exit code sends the output back to the "implement" node. Changes to the script only take effect once merged into the target branch, so the script must work from the current directory and must not rely on its own path. If a test has to be excluded, list it in the script's comments with the reason.

Community contributions do not require private source manifests or hash updates. Maintainers keep source synchronization records locally, separate from the public CI checks. See [the maintenance policy](docs/upstream-sync.md).

Run the [public browser acceptance suite](e2e/public/README.md) for authentication, preferences or execution changes. Paid model and external knowledge tests require explicit operator configuration; state passed, failed, skipped and not-run checks accurately.

Before each release, and whenever the pinned kiro-cli (`KiroCLIVersion` in `internal/runtime/kiro_login.go` and the `Dockerfile`) changes, run the real Kiro CLI approval test by hand. CI does not run it, because it needs `kiro-cli login` on the machine and spends a few Kiro credits. It checks that a pending approval blocks a shell command past Kiro's 60-second default hook timeout, that a denied command does not run, and that an approved one does:

```bash
LOOPFORGE_KIRO_E2E=1 go test ./internal/runtime/ -run TestKiroConversationApprovalHookRealCLI -count=1 -v -timeout 15m
```

Platform pull requests run the `core`, `browser`, and `first-run` jobs. The stable check `platform-required` fails unless all three succeed. A repository administrator has to mark `platform-required` as a required status check in GitHub branch protection. This repository does not change that GitHub setting.

For LoopForge Local, work inside `local/` and follow [local agent instructions](local/AGENTS.md). Identify whether a change affects Classic, Portable or shared behavior contracts. Keep these workflow assets independent from the platform and each other. Change Portable assets in `local/skills/`; rebuild generated Classic Cursor/Claude bundles with `python3 scripts/build-classic-hosts.py --write` from `local/`. Keep workflow templates generic; optional tools under `local/tools/` are distributed separately.

Run local checks from `local/`:

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

The [local E2E suite](local/tests/e2e/README.md) uses a fake host and recorded outputs, without model credentials. Describe the impact on each affected edition. Preserve existing permission behavior and user-file protection. Commit messages should follow Conventional Commits.

Public pull requests must not contain credentials, private infrastructure, employee access defaults or production datasets. Preserve existing user files. Keep source attribution and third-party licenses. Describe the problem, resulting behavior and the validation actually performed in the PR.
