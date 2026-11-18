#!/usr/bin/env bash
# Explicit local installation, schema initialization and catalog installation.
# The database runs in Compose (PostgreSQL, or MySQL with LOOPFORGE_DATABASE=mysql
# on the first run); the platform runs natively from this checkout.
set -euo pipefail
cd "$(dirname "$0")/.."
bash scripts/configure.sh
source scripts/load-env.sh
load_env
source scripts/docker-access.sh
source scripts/database.sh
mkdir -p .local/bin .local/workspaces
docker compose up -d --wait "$LOOPFORGE_DATABASE"
bash scripts/build-binaries.sh
if [[ ! -f .local/admin-password ]]; then
  (umask 077; python3 -c 'import secrets; print(secrets.token_urlsafe(18))' > .local/admin-password)
fi
# The account command opens the database first, so gorm has built the tables
# the baseline and seed below need.
.local/bin/loopforge-account -user admin -admin < .local/admin-password
bash scripts/install-schema-extensions.sh
printf 'Open http://localhost:%s — account: admin — password: .local/admin-password\n' "$AGENTFLOW_HTTP_PORT"
export AGENTFLOW_RUNNER_BINARY="$PWD/.local/bin/agentflow-runner-{os}-{arch}"
export AGENTFLOW_GATE_MCP_BINARY="$PWD/.local/bin/agentflow-gate-mcp-{os}-{arch}"
printf '%s\n' "$$" > .local/platform.pid
exec .local/bin/agentflow -addr "${LOOPFORGE_BIND:-0.0.0.0}:$AGENTFLOW_HTTP_PORT" -workspaces "$PWD/.local/workspaces"
