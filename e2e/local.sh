#!/usr/bin/env bash
# Runs the live e2e suite against a throwaway AgentFlow built from THIS
# checkout, so unmerged changes can be exercised end to end.
#
#   ./local.sh                     API scenarios (default)
#   ./local.sh api/30-runs.test.mjs api/35-runner-mode.test.mjs
#   ./local.sh all                 API, then UI
#
# It starts its own database container (PostgreSQL, or MySQL with
# AGENTFLOW_E2E_LOCAL_DATABASE=mysql) and platform process, seeds a user with an
# API token, the sandboxes the suite expects and a local bare Git repository
# for the repository-bound scenarios, and removes all of it afterwards.
#
# Optional environment:
#   AGENTFLOW_E2E_EXECUTOR           CLI executor ID (default codebuddy); kiro
#                                    runs the real-model scenarios on this
#                                    machine's Kiro CLI subscription login
#                                    (kiro-cli login), no key needed; the
#                                    assistant then runs on
#                                    AGENTFLOW_E2E_ASSISTANT_MODEL (default
#                                    kiro:claude-haiku-5.5)
#   AGENTFLOW_E2E_CLI_KEY            enables real-model scenarios for that CLI
#   AGENTFLOW_E2E_CLI_BASE_URL       optional custom model gateway
#   AGENTFLOW_E2E_CODEBUDDY_API_KEY  legacy CodeBuddy credential input
#   AGENTFLOW_E2E_CODEBUDDY_INTERNET CodeBuddy network environment (default
#                                    empty: the international site)
#   AGENTFLOW_E2E_LOCAL_PORT         platform port (default 18090)
#   AGENTFLOW_E2E_LOCAL_DATABASE     postgres (default) or mysql
#   AGENTFLOW_E2E_LOCAL_POSTGRES_PORT PostgreSQL port (default 35416)
#   AGENTFLOW_E2E_LOCAL_MYSQL_PORT   MySQL port (default 33316)
#   AGENTFLOW_E2E_LOCAL_POSTGRES_CONTAINER, AGENTFLOW_E2E_LOCAL_MYSQL_CONTAINER
#                                    database container name (default
#                                    agentflow-e2e-local-postgres / -mysql); set
#                                    it when several checkouts run the suite at once
#   AGENTFLOW_E2E_MODEL_SANDBOX      sandbox for model scenarios (default local:
#                                    the seeded Docker image has no model CLI)
#   AGENTFLOW_E2E_KEEP=1             keep the platform, database and files; the
#                                    exports needed to rerun single files against
#                                    it are written to $WORK/env (mode 600)
set -uo pipefail
cd "$(dirname "$0")"
E2E_DIR=$(pwd)
ROOT=$(cd .. && pwd)

PORT=${AGENTFLOW_E2E_LOCAL_PORT:-18090}
DATABASE=${AGENTFLOW_E2E_LOCAL_DATABASE:-postgres}
WORK=$(mktemp -d /tmp/agentflow-e2e-local.XXXXXX)
BASE=http://127.0.0.1:$PORT
PID=
case "$DATABASE" in
  postgres)
    DB_PORT=${AGENTFLOW_E2E_LOCAL_POSTGRES_PORT:-35416}
    DB_CONTAINER=${AGENTFLOW_E2E_LOCAL_POSTGRES_CONTAINER:-agentflow-e2e-local-postgres}
    DSN="postgres://postgres:root@127.0.0.1:$DB_PORT/agentflow?sslmode=disable"
    NOW='now()' TOMORROW="now() + interval '1 day'"
    ;;
  mysql)
    DB_PORT=${AGENTFLOW_E2E_LOCAL_MYSQL_PORT:-33316}
    DB_CONTAINER=${AGENTFLOW_E2E_LOCAL_MYSQL_CONTAINER:-agentflow-e2e-local-mysql}
    DSN="root:root@tcp(127.0.0.1:$DB_PORT)/agentflow?parseTime=true&charset=utf8mb4"
    NOW='NOW(3)' TOMORROW='DATE_ADD(NOW(3), INTERVAL 1 DAY)'
    ;;
  *) echo "AGENTFLOW_E2E_LOCAL_DATABASE must be postgres or mysql" >&2; exit 1 ;;
esac

teardown() {
  if [ "${AGENTFLOW_E2E_KEEP:-0}" = 1 ]; then
    echo "AGENTFLOW_E2E_KEEP=1: platform $BASE (pid $PID), database $DB_CONTAINER and $WORK left in place; source $WORK/env to rerun"
    return
  fi
  [ -n "$PID" ] && kill "$PID" 2>/dev/null
  # Sandbox containers bind workspaces under $WORK.
  for c in $(docker ps -aq --filter name=agentflow-sbx 2>/dev/null); do
    docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' "$c" 2>/dev/null | grep -q "$WORK" && docker rm -f "$c" >/dev/null
  done
  docker rm -f "$DB_CONTAINER" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap teardown EXIT

db_exec() {
  if [ "$DATABASE" = postgres ]; then
    docker exec -i "$DB_CONTAINER" psql -X -q -v ON_ERROR_STOP=1 -U postgres -d agentflow
  else
    docker exec -i "$DB_CONTAINER" mysql -uroot -proot agentflow 2> >(grep -v 'Using a password' >&2)
  fi
}

if curl -s -o /dev/null --max-time 2 "$BASE/api/health"; then
  echo "port $PORT is already serving something; set AGENTFLOW_E2E_LOCAL_PORT" >&2
  exit 1
fi

echo "== build $ROOT"
(cd "$ROOT" && go build -o "$WORK/agentflow" ./cmd/agentflow && go build -o "$WORK/agentflow-runner" ./cmd/agentflow-runner) || exit 1

echo "== $DATABASE on 127.0.0.1:$DB_PORT"
docker rm -f "$DB_CONTAINER" >/dev/null 2>&1
# Both images first run a socket-only bootstrap server; wait for the real one on TCP.
if [ "$DATABASE" = postgres ]; then
  docker run -d --name "$DB_CONTAINER" -p "127.0.0.1:$DB_PORT:5432" -e POSTGRES_PASSWORD=root -e POSTGRES_DB=agentflow \
    -e POSTGRES_INITDB_ARGS='--encoding=UTF8 --locale=C.UTF-8' postgres:17 >/dev/null || exit 1
  until docker exec "$DB_CONTAINER" pg_isready -q -h 127.0.0.1 -U postgres -d agentflow; do sleep 1; done
else
  docker run -d --name "$DB_CONTAINER" -p "127.0.0.1:$DB_PORT:3306" \
    -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=agentflow mysql:8.0 >/dev/null || exit 1
  until docker exec "$DB_CONTAINER" mysql -uroot -proot -h127.0.0.1 --protocol=tcp -e 'SELECT 1' agentflow >/dev/null 2>&1; do sleep 1; done
fi

echo "== platform on $BASE"
HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
# AGENTFLOW_LOCAL_LOGIN_USER enables the loopback-only acceptance login, so the
# session-only endpoints (personal API tokens, logout) can be exercised.
AGENTFLOW_SESSION_SECRET=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n') \
AGENTFLOW_AUTH_REQUIRED=true \
AGENTFLOW_LOCAL_LOGIN_USER=e2e-local \
AGENTFLOW_RUNNER_BINARY="$WORK/agentflow-runner" \
AGENTFLOW_PLATFORM_MCP_URL="http://${HOST_IP:-127.0.0.1}:$PORT" \
  "$WORK/agentflow" -addr "0.0.0.0:$PORT" -workspaces "$WORK/workspaces" -database-dsn "$DSN" >"$WORK/platform.log" 2>&1 &
PID=$!
for _ in $(seq 60); do curl -sf "$BASE/api/health" >/dev/null && break; sleep 1; done
if ! curl -sf "$BASE/api/health" >/dev/null || ! kill -0 "$PID" 2>/dev/null; then
  tail -30 "$WORK/platform.log"
  exit 1
fi
for _ in $(seq 60); do echo 'SELECT 1 FROM api_tokens LIMIT 1' | db_exec >/dev/null 2>&1 && break; sleep 1; done
for _ in $(seq 60); do echo 'SELECT 1 FROM run_telemetry, run_logs LIMIT 1' | db_exec >/dev/null 2>&1 && break; sleep 1; done

# Deployments apply migrations/ by hand. AutoMigrate creates every table and
# column except the generated columns of the baseline, which the run detail's
# telemetry filter (adapter=live) and the analytics coverage query need.
if [ "$DATABASE" = postgres ]; then BASELINE=migrations/postgres/001_baseline_extensions.sql; else BASELINE=migrations/001_run_obs_generated_columns.sql; fi
echo "== $BASELINE"
db_exec <"$ROOT/$BASELINE" || exit 1

echo "== seed"
new_token() { echo "afp_$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')"; }
TOKEN=$(new_token)
HASH=$(printf %s "$TOKEN" | sha256sum | cut -d' ' -f1)
# User B is an ordinary (non-admin) user for the authorization scenarios.
TOKEN_B=$(new_token)
HASH_B=$(printf %s "$TOKEN_B" | sha256sum | cut -d' ' -f1)
db_exec <<SQL || exit 1
INSERT INTO users (id, staff_id, login_name, chinese_name, platform_role, created_at, updated_at)
  VALUES (900001, 900001, 'e2e-local', 'E2E 本地', 'admin', $NOW, $NOW),
         (900002, 900002, 'e2e-local-b', 'E2E 本地 B', '', $NOW, $NOW);
INSERT INTO api_tokens (id, user_id, name, prefix, token_hash, expires_at, created_at)
  VALUES ('tok-e2e-local', 900001, 'e2e-local', '${TOKEN:0:12}', '$HASH', $TOMORROW, $NOW),
         ('tok-e2e-local-b', 900002, 'e2e-local-b', '${TOKEN_B:0:12}', '$HASH_B', $TOMORROW, $NOW);
SQL
export AGENTFLOW_E2E_EXECUTOR=${AGENTFLOW_E2E_EXECUTOR:-codebuddy}
if [ "$AGENTFLOW_E2E_EXECUTOR" = kiro ]; then
  # Kiro CLI signs in with this machine's kiro-cli login; nothing to seed.
  export AGENTFLOW_E2E_MODEL=${AGENTFLOW_E2E_MODEL:-1}
  export AGENTFLOW_E2E_ASSISTANT_MODEL=${AGENTFLOW_E2E_ASSISTANT_MODEL:-kiro:claude-haiku-5.5}
elif [ -n "${AGENTFLOW_E2E_CLI_KEY:-}" ] || { [ "$AGENTFLOW_E2E_EXECUTOR" = codebuddy ] && [ -n "${AGENTFLOW_E2E_CODEBUDDY_API_KEY:-}" ]; }; then
  # Seed disposable credentials over stdin. Hex literals preserve the exact
  # key and URL without passing either to the database client as an argument.
  if ! DATABASE="$DATABASE" NOW="$NOW" python3 - <<'PY_CLI' | db_exec >/dev/null 2>&1
import json
import os

executor = os.environ["AGENTFLOW_E2E_EXECUTOR"]
key = os.environ.get("AGENTFLOW_E2E_CLI_KEY", "") or os.environ.get("AGENTFLOW_E2E_CODEBUDDY_API_KEY", "")
base = os.environ.get("AGENTFLOW_E2E_CLI_BASE_URL", "")
if executor == "codebuddy":
    fields = {"codebuddy_api_key": key, "codebuddy_internet_environment": os.environ.get("AGENTFLOW_E2E_CODEBUDDY_INTERNET", "")}
elif executor in ("codex", "claude"):
    prefix = "codex" if executor == "codex" else "claude_code"
    fields = {prefix + "_api_key": key, prefix + "_base_url": base}
elif executor in ("opencode", "cursor", "agy", "kimi", "pi", "dsh", "zcode"):
    fields = {"agent_cli_credentials": json.dumps({executor: {"apiKey": key, "baseUrl": base}})}
else:
    raise SystemExit("Unsupported model executor")
postgres = os.environ["DATABASE"] == "postgres"
def literal(value):
    raw = value.encode().hex()
    return f"convert_from('\\x{raw}'::bytea, 'UTF8')" if postgres else f"X'{raw}'"
columns = ", ".join(fields)
values = ", ".join(literal(value) for value in fields.values())
now = os.environ["NOW"]
print(f"INSERT INTO user_configs (user_id, {columns}, created_at, updated_at) VALUES (900001, {values}, {now}, {now});")
PY_CLI
  then
    echo "Seeding the selected CLI credential failed." >&2
    exit 1
  fi
else
  export AGENTFLOW_E2E_MODEL=0
fi
unset AGENTFLOW_E2E_CODEBUDDY_API_KEY AGENTFLOW_E2E_CLI_KEY

api() {
  local code
  code=$(curl -s -o "$WORK/seed.out" -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -X "$1" "$BASE$2" --data-raw "$3")
  case "$code" in 2??) ;; *) echo "seed $1 $2 failed ($code): $(cat "$WORK/seed.out" 2>/dev/null)"; exit 1 ;; esac
}
api POST /api/projects '{"id":"e2e-auto","key":"E2E","name":"E2E 自动化测试（勿用）","color":"#8b5cf6","ownerId":900001}'
api PUT /api/dockerfiles '{"id":"generic-backend","name":"generic backend","projectId":"e2e-auto","content":"FROM python:3.12-slim\nRUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*\n"}'
api PUT /api/sandboxes '{"id":"generic-docker","name":"Generic Docker","projectId":"e2e-auto","driver":"docker","dockerfileId":"generic-backend","readOnlyRootFS":false}'
api PUT /api/sandboxes '{"id":"local","name":"Local","projectId":"e2e-auto","driver":"local"}'

REPO="$WORK/repo"
mkdir -p "$REPO/src" && git -C "$REPO/src" init -q -b main &&
  echo baseline >"$REPO/src/README.md" &&
  git -C "$REPO/src" -c user.name=e2e -c user.email=e2e@local add README.md &&
  git -C "$REPO/src" -c user.name=e2e -c user.email=e2e@local commit -qm baseline &&
  git clone -q --bare "$REPO/src" "$REPO/remote.git" || exit 1

export AGENTFLOW_E2E_BASE=$BASE AGENTFLOW_E2E_TOKEN=$TOKEN AGENTFLOW_E2E_TOKEN_B=$TOKEN_B
export AGENTFLOW_E2E_REPO_CLONE_URL="$REPO/remote.git" AGENTFLOW_E2E_REPO_BRANCH=main
export AGENTFLOW_E2E_MODEL_SANDBOX=${AGENTFLOW_E2E_MODEL_SANDBOX:-local}
# A fresh database has no data-rich project to tour; tour the e2e project.
export AGENTFLOW_E2E_VIEW_PROJECT=${AGENTFLOW_E2E_VIEW_PROJECT:-e2e-auto}
if [ "${AGENTFLOW_E2E_KEEP:-0}" = 1 ]; then
  (umask 077 && printf 'export AGENTFLOW_E2E_BASE=%q AGENTFLOW_E2E_TOKEN=%q AGENTFLOW_E2E_TOKEN_B=%q AGENTFLOW_E2E_REPO_CLONE_URL=%q AGENTFLOW_E2E_REPO_BRANCH=main AGENTFLOW_E2E_MODEL_SANDBOX=%q AGENTFLOW_E2E_MODEL=%q AGENTFLOW_E2E_VIEW_PROJECT=%q AGENTFLOW_E2E_EXECUTOR=%q AGENTFLOW_E2E_MODEL_ID=%q\n' \
    "$BASE" "$TOKEN" "$TOKEN_B" "$AGENTFLOW_E2E_REPO_CLONE_URL" "$AGENTFLOW_E2E_MODEL_SANDBOX" "${AGENTFLOW_E2E_MODEL:-1}" "$AGENTFLOW_E2E_VIEW_PROJECT" "$AGENTFLOW_E2E_EXECUTOR" "${AGENTFLOW_E2E_MODEL_ID:-}" >"$WORK/env")
fi

status=0
run_api() {
  local files=("$@")
  [ ${#files[@]} -gt 0 ] || files=('api/*.test.mjs')
  node --test --test-concurrency=1 --test-reporter=spec "${files[@]}" || status=1
}
case "${1:-api}" in
  api) run_api ;;
  all) run_api; npx playwright test || status=1 ;;
  *) run_api "$@" ;;
esac
exit $status
