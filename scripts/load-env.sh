# Sourced helper: load_env [file] exports the KEY=VALUE lines of a .env file
# (default .env) without running it as a script, so an unquoted
# `LOOPFORGE_KIRO_DATA_DIR=/Users/x/Library/Application Support/kiro-cli`
# loads the way Compose reads it instead of failing. Like Compose: comment and
# blank lines are skipped, an optional `export ` prefix is dropped, a value in
# matching single or double quotes is taken literally between them (a trailing
# ` # comment` after the quote is ignored), an unquoted value is trimmed and
# loses a ` # comment` tail. Nothing is expanded or executed: `$(...)`, `$VAR`
# and `~` stay as written.
# ponytail: no `${VAR}` interpolation or `\n` escapes inside double quotes,
# which Compose does; the generated .env needs neither. Add them here if it does.
load_env() {
  local file="${1:-.env}" line key value
  local assignment='^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=[[:space:]]*(.*)$'
  local double='^"(.*)"[[:space:]]*(#.*)?$' single="^'(.*)'[[:space:]]*(#.*)?\$"
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ $assignment ]] || continue
    key="${BASH_REMATCH[2]}" value="${BASH_REMATCH[3]}"
    if [[ "$value" =~ $double || "$value" =~ $single ]]; then
      value="${BASH_REMATCH[1]}"
    else
      value="${value%%[[:space:]]#*}"
      value="${value%"${value##*[![:space:]]}"}"
    fi
    export "$key=$value"
  done < "$file"
}
