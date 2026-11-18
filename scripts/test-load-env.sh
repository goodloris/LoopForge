#!/usr/bin/env bash
# Checks scripts/load-env.sh: .env values load the way Compose reads them and
# nothing in .env is executed. Run: bash scripts/test-load-env.sh
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
source "$root/scripts/load-env.sh"
fails=0
check() {
  if [[ "${!1-<unset>}" == "$2" ]]; then echo "ok   $1"; else echo "FAIL $1: got [${!1-<unset>}], want [$2]"; fails=$((fails+1)); fi
}

cat > "$tmp/.env" <<'ENV'
# LOOPFORGE_COMMENTED=yes
   # indented comment

UNQUOTED_SPACE=/Users/x/Library/Application Support/kiro-cli
DOUBLE="/Users/x/Library/Application Support/kiro-cli"
SINGLE='/Users/x/Library/Application Support/kiro-cli'
TRAILING=value with tail   # a comment
QUOTED_COMMENT="a # b" # c
export EXPORTED=yes
SUBSHELL=$(touch PWNED)
SUBSHELL_DOUBLE="$(touch PWNED)"
BACKTICK=`touch PWNED`
TILDE=~/kiro
EMPTY=
NOT AN ASSIGNMENT
ENV
printf 'CRLF=line\r\n' >> "$tmp/.env"
load_env "$tmp/.env"
check UNQUOTED_SPACE '/Users/x/Library/Application Support/kiro-cli'
check DOUBLE '/Users/x/Library/Application Support/kiro-cli'
check SINGLE '/Users/x/Library/Application Support/kiro-cli'
check TRAILING 'value with tail'
check QUOTED_COMMENT 'a # b'
check EXPORTED yes
check SUBSHELL '$(touch PWNED)'
check SUBSHELL_DOUBLE '$(touch PWNED)'
check BACKTICK '`touch PWNED`'
check TILDE '~/kiro'
check EMPTY ''
check CRLF line
check LOOPFORGE_COMMENTED '<unset>'
if [[ -e "$tmp/PWNED" || -e PWNED ]]; then echo 'FAIL .env command was executed'; fails=$((fails+1)); else echo 'ok   nothing executed'; fi
# Exported, so child processes (docker compose, python) see the values.
[[ "$(bash -c 'printf %s "$DOUBLE"')" == "$DOUBLE" ]] && echo 'ok   exported' || { echo 'FAIL not exported'; fails=$((fails+1)); }

# configure.sh quotes a value with spaces when it writes .env, and it loads back.
mkdir -p "$tmp/inst/scripts"
cp "$root/scripts/configure.sh" "$tmp/inst/scripts/"
LOOPFORGE_DATA_DIR="$tmp/inst/data dir" bash "$tmp/inst/scripts/configure.sh" >/dev/null
unset LOOPFORGE_DATA_DIR
grep -q "^LOOPFORGE_DATA_DIR='" "$tmp/inst/.env" && echo 'ok   configure.sh quotes spaces' || { echo 'FAIL configure.sh wrote an unquoted value'; fails=$((fails+1)); }
load_env "$tmp/inst/.env"
check LOOPFORGE_DATA_DIR "$tmp/inst/data dir"

(( fails == 0 )) || { echo "$fails check(s) failed"; exit 1; }
echo 'load-env: all checks passed'
