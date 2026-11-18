#!/usr/bin/env bash
# The Compose database this installation uses, for scripts run from the
# repository root. Sourced after docker-access.sh. LOOPFORGE_DATABASE in .env
# (written by configure.sh) wins over the environment: once a database holds
# data, switching it is an edit to .env, not a variable on one command.

loopforge_database() {
  local stored=''
  [[ -f .env ]] && stored=$(sed -n 's/^LOOPFORGE_DATABASE=//p' .env | tail -n 1 | tr -d "\"' ")
  LOOPFORGE_DATABASE="${stored:-${LOOPFORGE_DATABASE:-postgres}}"
  case "$LOOPFORGE_DATABASE" in
    postgres|mysql) ;;
    *) printf '\n✗ LOOPFORGE_DATABASE must be postgres or mysql, not %s.\n' "$LOOPFORGE_DATABASE" >&2; exit 1 ;;
  esac
}

# SQL on stdin, run as the platform's database user; stops at the first error.
db_sql() {
  if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then
    docker_access compose exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U loopforge -d loopforge "$@"
  else
    docker_access compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql "$@" -u loopforge loopforge' mysql "$@"
  fi
}

# Same, printing bare values: one row per line, no headers.
db_query() {
  if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then db_sql -At; else db_sql --batch --skip-column-names; fi
}

loopforge_database
