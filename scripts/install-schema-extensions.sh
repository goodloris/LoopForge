#!/usr/bin/env bash
# Bring the Compose database up to what this release reads but gorm
# AutoMigrate cannot create, after the platform is healthy (gorm has built the
# tables). PostgreSQL: migrations/postgres/001_baseline_extensions.sql, then
# migrations/postgres/seed/*.sql (ON CONFLICT DO NOTHING). MySQL: the
# generated columns from migration 001, the INSERT IGNORE starter catalog and
# the built-in Dockerfile updates from migrations 031 and 034. All steps are
# safe to repeat, so the installer and `./start.sh pull` run it every time. It
# ends by naming the manual migrations (033, 035) a MySQL database still lacks.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/docker-access.sh
source scripts/database.sh

if [[ "$LOOPFORGE_DATABASE" == postgres ]]; then
  baseline=migrations/postgres/001_baseline_extensions.sql
  seed=(migrations/postgres/seed/*.sql)
else
  baseline=migrations/001_run_obs_generated_columns.sql
  seed=(migrations/seed/*.sql migrations/031_generic_backend_openssh.sql migrations/036_generic_backend_pip.sql)
fi
if ! bash scripts/install-observability-schema.sh; then
  printf '\n✗ Could not add the generated columns run details need.\n' >&2
  printf '  Run %s against the LoopForge database, then retry.\n' "$baseline" >&2
  exit 1
fi
cat "${seed[@]}" | db_sql
bash scripts/manual-migration-hints.sh
