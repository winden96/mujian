#!/usr/bin/env bash
set -Eeuo pipefail

test_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script=$(cd "$test_dir/.." && pwd)/sqlite-to-postgres.sh

fail() {
  echo "sqlite-to-postgres static test failed: $*" >&2
  exit 1
}

require_literal() {
  local value=$1
  grep -Fq -- "$value" "$script" || fail "missing: $value"
}

bash -n "$script"

orphan_checks=(
  ability_channel price_channel checkin_user
  log_user log_channel log_token
  midjourney_user midjourney_channel model_vendor
  project_user scene_project shot_project shot_scene
  session_project session_user message_project message_user message_session
  generation_project generation_user generation_session reference_generation
  seedance_project seedance_user seedance_session seedance_first_frame preference_user
  passkey_user quota_user redemption_creator redemption_user
  order_user order_plan subscription_user subscription_plan
  preconsume_user preconsume_subscription token_user task_user task_channel topup_user
  two_fa_user two_fa_backup_user oauth_binding_user oauth_binding_provider user_inviter
)
for check in "${orphan_checks[@]}"; do
  require_literal "verify_orphans $check "
done

sensitive_sets=(
  channels users tokens options custom_oauth two_fa two_fa_backup passkeys
  oauth_bindings redemptions subscription_provider_payload
)
for set_name in "${sensitive_sets[@]}"; do
  require_literal "verify_sensitive_set $set_name "
done
require_literal 'verify_task_private_keys'

require_literal 'up -d --wait --wait-timeout 120 postgres'
require_literal 'pgloader executable is not owned by the Ubuntu pgloader package'
require_literal 'expected Ubuntu pgloader package 3.6.10-1build2'
require_literal "\"\$pgloader_path\" --version"
require_literal 'pgloader_args=(--on-error-stop)'
require_literal "column \$qualified to boolean drop default using sql-server-bit-to-boolean"
require_literal "column \$qualified to bytea using byte-vector-to-bytea"
require_literal 'pgloader reported an ERROR/FATAL/CRITICAL result'
require_literal "\"\$pgloader_path\" \"\${pgloader_args[@]}\" \"\$sqlite_uri\" \"\$postgres_dsn\""
require_literal "validate_sqlite_contract \"\$sqlite_db\""
require_literal "validate_sqlite_contract \"\$snapshot\""
require_literal 'abilities_pk_columns=group,model,channel_id'
require_literal "abilities|\$abilities_pk_columns"
require_literal "= '\$abilities_pk_columns'"
require_literal "!= \"\$abilities_pk_columns\""
require_literal 'PRIMARY KEY USING INDEX'
require_literal 'PostgreSQL primary-key schema does not match the SQLite source contract'
[[ $(grep -Foc 'group,model,channel_id' "$script") == 1 ]] || \
  fail 'abilities primary-key columns must have a single production definition'
require_literal 'rehearsal must not target the configured production database'
require_literal 'cutover must target the configured production database'
require_literal 'production_database=mujian'
require_literal "rehearsal must not target production database \$production_database"
require_literal "cutover must target production database \$production_database"
if grep -Fq 'mujian-migrate' "$script"; then
  require_literal '--expect-database'
fi

guard_line=$(grep -nF -- "if [[ -z \${MIGRATION_CONFIRM_DATABASE:-}" "$script" | head -n 1 | cut -d: -f1)
drop_line=$(grep -nF -- "-c 'DROP SCHEMA IF EXISTS public CASCADE'" "$script" | head -n 1 | cut -d: -f1)
[[ -n $guard_line && -n $drop_line && $guard_line -lt $drop_line ]] || \
  fail 'database-name guard must execute before DROP SCHEMA'

mode_guard_line=$(grep -nF -- "if [[ \$mode == rehearsal && \$target_database == \"\$production_database\" ]]" "$script" | tail -n 1 | cut -d: -f1)
[[ -n $mode_guard_line && $mode_guard_line -lt $drop_line ]] || \
  fail 'mode-specific production database guard must execute before DROP SCHEMA'

pgloader_guard_line=$(grep -nF -- "pgloader_owner=\$(dpkg-query -S \"\$pgloader_path\"" "$script" | head -n 1 | cut -d: -f1)
compose_start_line=$(grep -nF -- "\"\${compose[@]}\" up -d --wait --wait-timeout 120 postgres" "$script" | head -n 1 | cut -d: -f1)
[[ -n $pgloader_guard_line && -n $compose_start_line && $pgloader_guard_line -lt $compose_start_line ]] || \
  fail 'pgloader trust gate must execute before Compose or PostgreSQL side effects'

sqlite_guard_line=$(grep -nF -- "validate_sqlite_contract \"\$sqlite_db\"" "$script" | head -n 1 | cut -d: -f1)
[[ -n $sqlite_guard_line && -n $compose_start_line && $sqlite_guard_line -lt $compose_start_line ]] || \
  fail 'SQLite schema/value contract must execute before Compose or PostgreSQL side effects'

temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
mkdir -p "$temporary/bin"
sqlite3_bin=$(command -v sqlite3)

"$sqlite3_bin" "$temporary/source.db" <<'SQL'
CREATE TABLE abilities (
  "group" varchar(64),
  model varchar(255),
  channel_id integer,
  enabled numeric,
  PRIMARY KEY ("group", model, channel_id)
);
CREATE TABLE channel_model_prices (id INTEGER PRIMARY KEY, available numeric NOT NULL DEFAULT false);
CREATE TABLE custom_oauth_providers (id INTEGER PRIMARY KEY, enabled numeric DEFAULT false);
CREATE TABLE logs (id INTEGER PRIMARY KEY, is_stream numeric);
CREATE TABLE mujian_user_preferences (id INTEGER PRIMARY KEY, onboarded numeric NOT NULL DEFAULT false);
CREATE TABLE passkey_credentials (
  id INTEGER PRIMARY KEY,
  clone_warning numeric,
  user_present numeric,
  user_verified numeric,
  backup_eligible numeric,
  backup_state numeric
);
CREATE TABLE subscription_plans (id INTEGER PRIMARY KEY, enabled numeric DEFAULT 1);
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY,
  unlimited_quota numeric,
  model_limits_enabled numeric,
  cross_group_retry numeric
);
CREATE TABLE two_fa_backup_codes (id INTEGER PRIMARY KEY, is_used numeric);
CREATE TABLE two_fas (id INTEGER PRIMARY KEY, is_enabled numeric);
CREATE TABLE channels (id INTEGER PRIMARY KEY, channel_info json);
CREATE TABLE prefill_groups (id INTEGER PRIMARY KEY, items json);
CREATE TABLE tasks (id INTEGER PRIMARY KEY, data json, private_data json, properties json);

INSERT INTO abilities("group", model, channel_id, enabled)
  VALUES ('default', 'model-a', 1, 0), ('default', 'model-b', 1, 1), ('default', 'model-c', 1, NULL);
INSERT INTO channel_model_prices(available) VALUES (1);
INSERT INTO custom_oauth_providers(enabled) VALUES (0);
INSERT INTO logs(is_stream) VALUES (1);
INSERT INTO mujian_user_preferences(onboarded) VALUES (0);
INSERT INTO passkey_credentials(
  clone_warning, user_present, user_verified, backup_eligible, backup_state
) VALUES (0, 1, 1, 0, 0);
INSERT INTO subscription_plans(enabled) VALUES (1);
INSERT INTO tokens(unlimited_quota, model_limits_enabled, cross_group_retry) VALUES (0, 1, 0);
INSERT INTO two_fa_backup_codes(is_used) VALUES (0);
INSERT INTO two_fas(is_enabled) VALUES (1);
INSERT INTO channels(channel_info) VALUES (CAST('{"is_multi_key":false}' AS BLOB));
INSERT INTO prefill_groups(items) VALUES (CAST('[]' AS BLOB));
INSERT INTO tasks(data, private_data, properties)
  VALUES (CAST('{}' AS BLOB), CAST('{"key":"fixture"}' AS BLOB), CAST('{}' AS BLOB));
SQL

cat >"$temporary/bin/pgloader" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == --version ]]; then
  echo 'pgloader version "3.6.7~devel"'
  exit 0
fi
printf '%s\n' "$@" >"$PGLOADER_ARGS_FILE"
: >"$PGLOADER_RAN_MARKER"
if [[ -n ${MOCK_PGLOADER_LEVEL:-} ]]; then
  printf '2026-08-22T00:00:00Z %s simulated pgloader failure\n' "$MOCK_PGLOADER_LEVEL"
fi
exit "${MOCK_PGLOADER_STATUS:-0}"
SH
cat >"$temporary/bin/dpkg-query" <<'SH'
#!/usr/bin/env bash
case ${1:-} in
  -S)
    printf '%s: %s\n' "${MOCK_PGLOADER_OWNER:-pgloader}" "${2:-}"
    ;;
  -W)
    printf '%s\n' "${MOCK_PGLOADER_PACKAGE_VERSION:-3.6.10-1build2}"
    ;;
  *)
    exit 96
    ;;
esac
SH
cat >"$temporary/bin/psql" <<'SH'
#!/usr/bin/env bash
: >"$PSQL_MARKER"
if [[ -e $PGLOADER_RAN_MARKER ]]; then
  : >"$POST_PGLOADER_PSQL_MARKER"
fi
if [[ $* == *'SELECT current_database()'* ]]; then
  echo "${MOCK_CURRENT_DATABASE:-mujian}"
  exit 0
fi
if [[ $* == *'DROP SCHEMA'* ]]; then
  : >"$DROP_MARKER"
  if [[ ${MOCK_ALLOW_DROP:-} == YES ]]; then
    exit 0
  fi
fi
exit 97
SH
cat >"$temporary/bin/realpath" <<'SH'
#!/usr/bin/env bash
if [[ ${1:-} == -m ]]; then
  shift
fi
printf '%s\n' "$1"
SH
chmod 0755 \
  "$temporary/bin/dpkg-query" "$temporary/bin/pgloader" \
  "$temporary/bin/psql" "$temporary/bin/realpath"

run_rehearsal() {
  local name=$1
  local target_database=$2
  shift 2
  rm -rf -- "${temporary:?}/$name"
  mkdir -p "$temporary/$name/backups" "$temporary/$name/reports"
  set +e
  env \
    PATH="$temporary/bin:$PATH" \
    SQLITE_DB="$temporary/source.db" \
    POSTGRES_DSN="postgresql://mujian@db/$target_database" \
    MIGRATION_CONFIRM_DSN="postgresql://mujian@db/$target_database" \
    MIGRATION_CONFIRM_DATABASE="$target_database" \
    MIGRATION_RESET_TARGET=YES \
    MIGRATION_BACKUP_DIR="$temporary/$name/backups" \
    MIGRATION_REPORT_DIR="$temporary/$name/reports" \
    PSQL_MARKER="$temporary/$name/psql" \
    DROP_MARKER="$temporary/$name/drop" \
    PGLOADER_RAN_MARKER="$temporary/$name/pgloader-ran" \
    PGLOADER_ARGS_FILE="$temporary/$name/pgloader-args" \
    POST_PGLOADER_PSQL_MARKER="$temporary/$name/post-pgloader-psql" \
    "$@" \
    "$script" rehearsal >"$temporary/$name/stdout" 2>"$temporary/$name/stderr"
  probe_status=$?
  set -e
}

run_rehearsal wrong-owner mujian MOCK_PGLOADER_OWNER=shadow
[[ $probe_status == 1 ]] || fail "wrong-owner probe returned $probe_status, expected 1"
grep -Fq 'pgloader executable is not owned by the Ubuntu pgloader package' \
  "$temporary/wrong-owner/stderr" || fail 'wrong-owner probe did not report ownership failure'
[[ ! -e $temporary/wrong-owner/psql && ! -e $temporary/wrong-owner/drop ]] || \
  fail 'wrong-owner probe reached PostgreSQL'

run_rehearsal wrong-version mujian MOCK_PGLOADER_PACKAGE_VERSION=3.6.9-1
[[ $probe_status == 1 ]] || fail "wrong-version probe returned $probe_status, expected 1"
grep -Fq 'expected Ubuntu pgloader package 3.6.10-1build2' \
  "$temporary/wrong-version/stderr" || fail 'wrong-version probe did not report version failure'
[[ ! -e $temporary/wrong-version/psql && ! -e $temporary/wrong-version/drop ]] || \
  fail 'wrong-version probe reached PostgreSQL'

cp "$temporary/source.db" "$temporary/extra-numeric.db"
"$sqlite3_bin" "$temporary/extra-numeric.db" 'ALTER TABLE abilities ADD COLUMN future_amount numeric;'
run_rehearsal extra-numeric mujian_rehearsal SQLITE_DB="$temporary/extra-numeric.db"
[[ $probe_status == 1 ]] || fail "extra-numeric probe returned $probe_status, expected 1"
grep -Fq 'SQLite numeric schema does not match the reviewed boolean-column contract' \
  "$temporary/extra-numeric/stderr" || fail 'extra-numeric probe did not report schema drift'
[[ ! -e $temporary/extra-numeric/psql ]] || fail 'extra-numeric probe reached PostgreSQL'

cp "$temporary/source.db" "$temporary/invalid-boolean.db"
"$sqlite3_bin" "$temporary/invalid-boolean.db" "UPDATE abilities SET enabled=2 WHERE model='model-a';"
run_rehearsal invalid-boolean mujian_rehearsal SQLITE_DB="$temporary/invalid-boolean.db"
[[ $probe_status == 1 ]] || fail "invalid-boolean probe returned $probe_status, expected 1"
grep -Fq 'SQLite boolean column contains a value outside NULL/0/1: abilities.enabled' \
  "$temporary/invalid-boolean/stderr" || fail 'invalid-boolean probe did not report invalid data'
[[ ! -e $temporary/invalid-boolean/psql ]] || fail 'invalid-boolean probe reached PostgreSQL'

cp "$temporary/source.db" "$temporary/invalid-json.db"
"$sqlite3_bin" "$temporary/invalid-json.db" "UPDATE channels SET channel_info='{}' WHERE id=1;"
run_rehearsal invalid-json mujian_rehearsal SQLITE_DB="$temporary/invalid-json.db"
[[ $probe_status == 1 ]] || fail "invalid-json probe returned $probe_status, expected 1"
grep -Fq 'SQLite JSON column contains non-BLOB or invalid JSON data: channels.channel_info' \
  "$temporary/invalid-json/stderr" || fail 'invalid-json probe did not report invalid data'
[[ ! -e $temporary/invalid-json/psql ]] || fail 'invalid-json probe reached PostgreSQL'

run_rehearsal production-guard mujian
if [[ $probe_status != 2 ]]; then
  sed 's/^/migration stderr: /' "$temporary/production-guard/stderr" >&2
  fail "explicit production DSN rehearsal returned $probe_status, expected 2"
fi
grep -Fq 'rehearsal must not target production database mujian' "$temporary/production-guard/stderr" || \
  fail 'explicit production DSN rehearsal did not report the production guard'
[[ -e $temporary/production-guard/psql ]] || fail 'positive package probe did not reach database identity check'
[[ ! -e $temporary/production-guard/drop ]] || fail 'explicit production DSN rehearsal reached DROP SCHEMA'

for level in ERROR FATAL CRITICAL; do
  name="pgloader-$(printf '%s' "$level" | tr '[:upper:]' '[:lower:]')"
  run_rehearsal "$name" mujian_rehearsal \
    MOCK_CURRENT_DATABASE=mujian_rehearsal \
    MOCK_ALLOW_DROP=YES \
    MOCK_PGLOADER_LEVEL="$level"
  [[ $probe_status == 1 ]] || fail "$level log probe returned $probe_status, expected 1"
  grep -Fq 'pgloader reported an ERROR/FATAL/CRITICAL result' \
    "$temporary/$name/stderr" || fail "$level log probe did not report pgloader severity"
  [[ -e $temporary/$name/pgloader-ran ]] || fail "$level log probe did not run pgloader"
  [[ ! -e $temporary/$name/post-pgloader-psql ]] || \
    fail "$level log probe reached PostgreSQL after pgloader"
done

boolean_specs=(
  abilities.enabled channel_model_prices.available custom_oauth_providers.enabled
  logs.is_stream mujian_user_preferences.onboarded
  passkey_credentials.backup_eligible passkey_credentials.backup_state
  passkey_credentials.clone_warning passkey_credentials.user_present
  passkey_credentials.user_verified subscription_plans.enabled tokens.cross_group_retry
  tokens.model_limits_enabled tokens.unlimited_quota two_fa_backup_codes.is_used
  two_fas.is_enabled
)
for spec in "${boolean_specs[@]}"; do
  grep -Fxq "column $spec to boolean drop default using sql-server-bit-to-boolean" \
    "$temporary/pgloader-error/pgloader-args" || fail "missing executed boolean cast: $spec"
done
json_specs=(
  channels.channel_info prefill_groups.items tasks.data tasks.private_data tasks.properties
)
for spec in "${json_specs[@]}"; do
  grep -Fxq "column $spec to bytea using byte-vector-to-bytea" \
    "$temporary/pgloader-error/pgloader-args" || fail "missing executed JSON cast: $spec"
done

echo "sqlite-to-postgres static checks passed"
