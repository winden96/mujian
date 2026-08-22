#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

mode=${1:-}
sqlite_db=${SQLITE_DB:-}
postgres_dsn=${POSTGRES_DSN:-}
confirm_dsn=${MIGRATION_CONFIRM_DSN:-}
backup_dir=${MIGRATION_BACKUP_DIR:-./deploy/backups}
report_dir=${MIGRATION_REPORT_DIR:-./deploy/reports}
env_file=${MUJIAN_ENV_FILE:-./deploy/runtime/mujian.env}
compose_file=${MUJIAN_COMPOSE_FILE:-./docker-compose.mujian.yml}
production_database=mujian
pgpass_file=

cleanup() {
  if [[ -n $pgpass_file && -f $pgpass_file ]]; then
    rm -f -- "$pgpass_file"
  fi
}
trap cleanup EXIT

usage() {
  echo "usage: SQLITE_DB=/absolute/one-api.db MIGRATION_RESET_TARGET=YES \\" >&2
  echo "       MIGRATION_CONFIRM_DATABASE=<database> $0 rehearsal|cutover" >&2
  echo "or provide POSTGRES_DSN plus an identical MIGRATION_CONFIRM_DSN" >&2
}

if [[ $mode != rehearsal && $mode != cutover ]]; then
  usage
  exit 2
fi
if [[ -z $sqlite_db ]]; then
  usage
  exit 2
fi
if [[ ${MIGRATION_RESET_TARGET:-} != YES ]]; then
  echo "refusing to replace the PostgreSQL public schema without MIGRATION_RESET_TARGET=YES" >&2
  exit 2
fi
if [[ $mode == cutover && ${MIGRATION_SOURCE_STOPPED:-} != YES ]]; then
  echo "cutover requires MIGRATION_SOURCE_STOPPED=YES after the source application is stopped" >&2
  exit 2
fi
if [[ ! -f $sqlite_db ]]; then
  echo "SQLite database not found: $sqlite_db" >&2
  exit 1
fi
sqlite_db=$(realpath "$sqlite_db")
backup_dir=$(realpath -m "$backup_dir")
report_dir=$(realpath -m "$report_dir")

for command in dpkg-query pgloader psql sqlite3 sha256sum; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command not found: $command" >&2
    exit 1
  fi
done

pgloader_path=$(realpath "$(type -P pgloader)")
if ! pgloader_owner=$(dpkg-query -S "$pgloader_path" 2>/dev/null) || \
  [[ $pgloader_owner != pgloader:\ * ]]; then
  echo "pgloader executable is not owned by the Ubuntu pgloader package: $pgloader_path" >&2
  exit 1
fi
pgloader_package_version=$(dpkg-query -W -f='${Version}' pgloader)
if [[ $pgloader_package_version != 3.6.10-1build2 ]]; then
  echo "expected Ubuntu pgloader package 3.6.10-1build2, got: $pgloader_package_version" >&2
  exit 1
fi
if ! "$pgloader_path" --version >/dev/null 2>&1; then
  echo "pgloader executable failed its version probe: $pgloader_path" >&2
  exit 1
fi

# GORM represents Go bools as SQLite numeric columns. Keep this allowlist
# exact so a future monetary/decimal numeric column can never be cast to bool
# by accident. Fields are: column|SQLite default|PostgreSQL default|nullable.
boolean_columns=(
  'abilities.enabled|<NULL>|<NULL>|YES'
  'channel_model_prices.available|false|false|NO'
  'custom_oauth_providers.enabled|false|false|YES'
  'logs.is_stream|<NULL>|<NULL>|YES'
  'mujian_user_preferences.onboarded|false|false|NO'
  'passkey_credentials.backup_eligible|<NULL>|<NULL>|YES'
  'passkey_credentials.backup_state|<NULL>|<NULL>|YES'
  'passkey_credentials.clone_warning|<NULL>|<NULL>|YES'
  'passkey_credentials.user_present|<NULL>|<NULL>|YES'
  'passkey_credentials.user_verified|<NULL>|<NULL>|YES'
  'subscription_plans.enabled|1|true|YES'
  'tokens.cross_group_retry|<NULL>|<NULL>|YES'
  'tokens.model_limits_enabled|<NULL>|<NULL>|YES'
  'tokens.unlimited_quota|<NULL>|<NULL>|YES'
  'two_fa_backup_codes.is_used|<NULL>|<NULL>|YES'
  'two_fas.is_enabled|<NULL>|<NULL>|YES'
)

# These GORM JSON values are SQLite blobs. pgloader must first preserve their
# bytes as bytea; a post-load transaction validates and converts them to json.
json_columns=(
  'channels.channel_info|YES'
  'prefill_groups.items|YES'
  'tasks.data|YES'
  'tasks.private_data|YES'
  'tasks.properties|YES'
)
abilities_pk_columns=group,model,channel_id

validate_sqlite_contract() {
  local database=$1
  local actual expected definition qualified source_default _ nullable
  local table column invalid

  actual=$(sqlite3 "$database" "
    SELECT m.name || '.' || p.name || '|' ||
      CASE WHEN p.dflt_value IS NULL THEN '<NULL>' ELSE lower(trim(p.dflt_value)) END || '|' ||
      CASE p.\"notnull\" WHEN 1 THEN 'NO' ELSE 'YES' END
    FROM sqlite_master m, pragma_table_info(m.name) p
    WHERE m.type = 'table' AND lower(trim(p.type)) = 'numeric'
    ORDER BY 1;")
  expected=$(
    for definition in "${boolean_columns[@]}"; do
      IFS='|' read -r qualified source_default _ nullable <<<"$definition"
      printf '%s|%s|%s\n' "$qualified" "$source_default" "$nullable"
    done
  )
  if [[ $actual != "$expected" ]]; then
    echo "SQLite numeric schema does not match the reviewed boolean-column contract" >&2
    exit 1
  fi

  actual=$(sqlite3 "$database" "
    SELECT m.name || '.' || p.name || '|' ||
      CASE WHEN p.dflt_value IS NULL THEN '<NULL>' ELSE lower(trim(p.dflt_value)) END || '|' ||
      CASE p.\"notnull\" WHEN 1 THEN 'NO' ELSE 'YES' END
    FROM sqlite_master m, pragma_table_info(m.name) p
    WHERE m.type = 'table' AND lower(trim(p.type)) = 'json'
    ORDER BY 1;")
  expected=$(
    for definition in "${json_columns[@]}"; do
      IFS='|' read -r qualified nullable <<<"$definition"
      printf '%s|<NULL>|%s\n' "$qualified" "$nullable"
    done
  )
  if [[ $actual != "$expected" ]]; then
    echo "SQLite JSON schema does not match the reviewed JSON-column contract" >&2
    exit 1
  fi

  actual=$(sqlite3 "$database" "
    SELECT table_name || '|' || group_concat(column_name, ',')
    FROM (
      SELECT m.name AS table_name, p.name AS column_name, p.pk
      FROM sqlite_master m, pragma_table_info(m.name) p
      WHERE m.type = 'table' AND m.name NOT GLOB 'sqlite_*' AND p.pk > 0
      ORDER BY m.name, p.pk
    ) primary_key_columns
    GROUP BY table_name
    HAVING count(*) > 1
    ORDER BY table_name;")
  if [[ $actual != "abilities|$abilities_pk_columns" ]]; then
    echo "SQLite composite-primary-key schema does not match the reviewed contract" >&2
    exit 1
  fi

  for definition in "${boolean_columns[@]}"; do
    qualified=${definition%%|*}
    table=${qualified%%.*}
    column=${qualified#*.}
    invalid=$(sqlite3 "$database" \
      "SELECT count(*) FROM \"$table\" WHERE \"$column\" IS NOT NULL AND (typeof(\"$column\") <> 'integer' OR \"$column\" NOT IN (0, 1));")
    if [[ $invalid != 0 ]]; then
      echo "SQLite boolean column contains a value outside NULL/0/1: $qualified" >&2
      exit 1
    fi
  done

  for definition in "${json_columns[@]}"; do
    qualified=${definition%%|*}
    table=${qualified%%.*}
    column=${qualified#*.}
    invalid=$(sqlite3 "$database" \
      "SELECT count(*) FROM \"$table\" WHERE \"$column\" IS NOT NULL AND (typeof(\"$column\") <> 'blob' OR NOT json_valid(\"$column\"));")
    if [[ $invalid != 0 ]]; then
      echo "SQLite JSON column contains non-BLOB or invalid JSON data: $qualified" >&2
      exit 1
    fi
  done
}

integrity=$(sqlite3 "$sqlite_db" 'PRAGMA integrity_check;')
if [[ $integrity != ok ]]; then
  echo "SQLite integrity_check failed: $integrity" >&2
  exit 1
fi
validate_sqlite_contract "$sqlite_db"

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$backup_dir" "$report_dir"
chmod 700 "$backup_dir" "$report_dir"
snapshot="$backup_dir/one-api-${mode}-${timestamp}.db"
report="$report_dir/sqlite-postgres-${mode}-${timestamp}.tsv"
pgloader_log="$report_dir/pgloader-${mode}-${timestamp}.log"

if [[ $snapshot == *"'"* ]]; then
  echo "backup path cannot contain a single quote: $snapshot" >&2
  exit 1
fi
sqlite3 "$sqlite_db" ".timeout 30000" ".backup '$snapshot'"
chmod 400 "$snapshot"
sha256sum "$snapshot" >"$snapshot.sha256"
validate_sqlite_contract "$snapshot"

read_env() {
  local key=$1
  sed -n "s/^${key}=//p" "$env_file" | tail -n 1 | tr -d '\r'
}

pgpass_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//:/\\:}
  printf '%s' "$value"
}

if [[ -z $postgres_dsn ]]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "required command not found for Compose database discovery: docker" >&2
    exit 1
  fi
  if [[ ! -f $env_file || ! -f $compose_file ]]; then
    echo "Compose env or file not found: $env_file / $compose_file" >&2
    exit 1
  fi
  postgres_user=$(read_env POSTGRES_USER)
  postgres_password=$(read_env POSTGRES_PASSWORD)
	configured_database=$(read_env POSTGRES_DB)
	postgres_user=${postgres_user:-mujian}
	configured_database=${configured_database:-mujian}
	if [[ $configured_database != "$production_database" ]]; then
		echo "POSTGRES_DB must equal the production database $production_database" >&2
		exit 2
	fi
  if [[ -z $postgres_password ]]; then
    echo "POSTGRES_PASSWORD is missing from $env_file" >&2
    exit 1
  fi
  target_database=${MIGRATION_TARGET_DB:-$configured_database}
  if [[ $mode == rehearsal && -z ${MIGRATION_TARGET_DB:-} ]]; then
    target_database=${configured_database}_rehearsal
  fi
  if [[ $mode == rehearsal && $target_database == "$configured_database" ]]; then
    echo "rehearsal must not target the configured production database" >&2
    exit 2
  fi
  if [[ $mode == cutover && $target_database != "$configured_database" ]]; then
    echo "cutover must target the configured production database" >&2
    exit 2
  fi
  if [[ ! $postgres_user =~ ^[A-Za-z_][A-Za-z0-9_]*$ || ! $target_database =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
    echo "PostgreSQL user and target database must be simple identifiers" >&2
    exit 1
  fi
  if [[ ${MIGRATION_CONFIRM_DATABASE:-} != "$target_database" ]]; then
    echo "set MIGRATION_CONFIRM_DATABASE=$target_database to confirm the derived Compose target" >&2
    exit 2
  fi

  compose=(docker compose --env-file "$env_file" -f "$compose_file")
  "${compose[@]}" up -d --wait --wait-timeout 120 postgres
  postgres_container=$("${compose[@]}" ps -q postgres)
  if [[ -z $postgres_container ]]; then
    echo "could not resolve the PostgreSQL Compose container" >&2
    exit 1
  fi
  if [[ $target_database != "$configured_database" ]]; then
    database_exists=$("${compose[@]}" exec -T postgres psql -U "$postgres_user" -d postgres -Atc \
      "SELECT 1 FROM pg_database WHERE datname = '$target_database'")
    if [[ $database_exists != 1 ]]; then
      "${compose[@]}" exec -T postgres createdb -U "$postgres_user" "$target_database"
    fi
  fi
  postgres_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}' \
    "$postgres_container" | awk 'NF {print; exit}')
  if [[ -z $postgres_ip ]]; then
    echo "could not resolve the PostgreSQL container IP" >&2
    exit 1
  fi
  pgpass_file=$(mktemp "${TMPDIR:-/tmp}/mujian-pgpass.XXXXXX")
  chmod 600 "$pgpass_file"
  printf '%s:%s:%s:%s:%s\n' \
    "$(pgpass_escape "$postgres_ip")" \
    5432 \
    "$(pgpass_escape "$target_database")" \
    "$(pgpass_escape "$postgres_user")" \
    "$(pgpass_escape "$postgres_password")" >"$pgpass_file"
  export PGPASSFILE=$pgpass_file
  postgres_dsn="postgresql://${postgres_user}@${postgres_ip}:5432/${target_database}"
elif [[ $postgres_dsn != "$confirm_dsn" ]]; then
  echo "POSTGRES_DSN requires an identical MIGRATION_CONFIRM_DSN" >&2
  exit 2
elif [[ $postgres_dsn =~ ^postgres(ql)?://[^/@]+:[^/@]*@ || $postgres_dsn =~ [\?\&][Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd]= ]]; then
  echo "POSTGRES_DSN must not contain an inline password; configure PGPASSFILE instead" >&2
  exit 2
fi

target_database=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc 'SELECT current_database()')
if [[ -z $target_database ]]; then
  echo "could not resolve the PostgreSQL target database" >&2
  exit 1
fi
if [[ -z ${MIGRATION_CONFIRM_DATABASE:-} || $target_database != "$MIGRATION_CONFIRM_DATABASE" ]]; then
	echo "set MIGRATION_CONFIRM_DATABASE=$target_database to confirm the connected PostgreSQL target" >&2
	exit 2
fi
if [[ $mode == rehearsal && $target_database == "$production_database" ]]; then
	echo "rehearsal must not target production database $production_database" >&2
	exit 2
fi
if [[ $mode == cutover && $target_database != "$production_database" ]]; then
	echo "cutover must target production database $production_database" >&2
	exit 2
fi

# A clean schema prevents stale application tables from surviving a rehearsal
# or cutover. The explicit confirmations and the mode-specific production
# database guard above protect this intentionally destructive operation.
psql "$postgres_dsn" -v ON_ERROR_STOP=1 \
  -c 'DROP SCHEMA IF EXISTS public CASCADE' \
  -c 'CREATE SCHEMA public'

sqlite_uri="sqlite://$snapshot"
pgloader_args=(--on-error-stop)
for definition in "${boolean_columns[@]}"; do
  qualified=${definition%%|*}
  pgloader_args+=(--cast "column $qualified to boolean drop default using sql-server-bit-to-boolean")
done
for definition in "${json_columns[@]}"; do
  qualified=${definition%%|*}
  pgloader_args+=(--cast "column $qualified to bytea using byte-vector-to-bytea")
done

pgloader_status=0
"$pgloader_path" "${pgloader_args[@]}" "$sqlite_uri" "$postgres_dsn" >"$pgloader_log" 2>&1 || pgloader_status=$?
if (( pgloader_status != 0 )); then
  echo "pgloader failed with status $pgloader_status; see $pgloader_log" >&2
  exit 1
fi
pgloader_log_status=0
grep -aEq '(^|[[:space:]])(ERROR|FATAL|CRITICAL)([[:space:]]|$)' "$pgloader_log" || pgloader_log_status=$?
if (( pgloader_log_status == 0 )); then
  echo "pgloader reported an ERROR/FATAL/CRITICAL result; see $pgloader_log" >&2
  exit 1
fi
if (( pgloader_log_status != 1 )); then
  echo "could not inspect the pgloader log safely: $pgloader_log" >&2
  exit 1
fi

source_required_not_null=$(sqlite3 "$snapshot" "
  SELECT m.name || '|' || p.name
  FROM sqlite_master m, pragma_table_info(m.name) p
  WHERE m.type = 'table' AND (p.\"notnull\" = 1 OR p.pk > 0)
  ORDER BY 1;")
mapfile -t required_not_null_columns <<<"$source_required_not_null"

abilities_primary_key=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
  "SELECT string_agg(kcu.column_name, ',' ORDER BY kcu.ordinal_position)
   FROM information_schema.table_constraints tc
   JOIN information_schema.key_column_usage kcu
     ON kcu.constraint_schema = tc.constraint_schema
       AND kcu.constraint_name = tc.constraint_name
       AND kcu.table_schema = tc.table_schema
       AND kcu.table_name = tc.table_name
   WHERE tc.constraint_schema = 'public' AND tc.table_name = 'abilities'
     AND tc.constraint_type = 'PRIMARY KEY'")
abilities_primary_key_sql=
if [[ -z $abilities_primary_key ]]; then
  abilities_unique_index=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT index_class.relname
     FROM pg_index index_info
     JOIN pg_class index_class ON index_class.oid = index_info.indexrelid
     WHERE index_info.indrelid = 'public.abilities'::regclass
       AND index_info.indisunique AND NOT index_info.indisprimary
       AND index_info.indpred IS NULL AND index_info.indexprs IS NULL
       AND (
         SELECT string_agg(attribute.attname, ',' ORDER BY key_column.ordinality)
         FROM unnest(index_info.indkey) WITH ORDINALITY AS key_column(attnum, ordinality)
         JOIN pg_attribute attribute
           ON attribute.attrelid = index_info.indrelid AND attribute.attnum = key_column.attnum
         WHERE key_column.ordinality <= index_info.indnkeyatts
       ) = '$abilities_pk_columns'")
  if [[ ! $abilities_unique_index =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
    echo "could not resolve exactly one reusable abilities composite unique index" >&2
    exit 1
  fi
  abilities_primary_key_sql="ALTER TABLE public.abilities ADD CONSTRAINT abilities_pkey PRIMARY KEY USING INDEX \"$abilities_unique_index\";"
elif [[ $abilities_primary_key != "$abilities_pk_columns" ]]; then
  echo "unexpected PostgreSQL abilities primary-key contract" >&2
  exit 1
fi

{
  printf '%s\n' \
    'BEGIN;' \
    "ALTER TABLE public.channels ALTER COLUMN channel_info TYPE json USING CASE WHEN channel_info IS NULL THEN NULL ELSE convert_from(channel_info, 'UTF8')::json END;" \
    "ALTER TABLE public.prefill_groups ALTER COLUMN items TYPE json USING CASE WHEN items IS NULL THEN NULL ELSE convert_from(items, 'UTF8')::json END;" \
    "ALTER TABLE public.tasks ALTER COLUMN data TYPE json USING CASE WHEN data IS NULL THEN NULL ELSE convert_from(data, 'UTF8')::json END;" \
    "ALTER TABLE public.tasks ALTER COLUMN private_data TYPE json USING CASE WHEN private_data IS NULL THEN NULL ELSE convert_from(private_data, 'UTF8')::json END;" \
    "ALTER TABLE public.tasks ALTER COLUMN properties TYPE json USING CASE WHEN properties IS NULL THEN NULL ELSE convert_from(properties, 'UTF8')::json END;" \
    'ALTER TABLE public.channel_model_prices ALTER COLUMN available SET DEFAULT false;' \
    'ALTER TABLE public.custom_oauth_providers ALTER COLUMN enabled SET DEFAULT false;' \
    'ALTER TABLE public.mujian_user_preferences ALTER COLUMN onboarded SET DEFAULT false;' \
    'ALTER TABLE public.subscription_plans ALTER COLUMN enabled SET DEFAULT true;'
  for qualified in "${required_not_null_columns[@]}"; do
    IFS='|' read -r table column <<<"$qualified"
    if [[ ! $table =~ ^[A-Za-z_][A-Za-z0-9_]*$ || ! $column =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "invalid SQLite NOT NULL identifier: $qualified" >&2
      exit 1
    fi
    printf 'ALTER TABLE public."%s" ALTER COLUMN "%s" SET NOT NULL;\n' "$table" "$column"
  done
  if [[ -n $abilities_primary_key_sql ]]; then
    printf '%s\n' "$abilities_primary_key_sql"
  fi
  printf 'COMMIT;\n'
} | psql "$postgres_dsn" -v ON_ERROR_STOP=1

expected_column_contract=$(sqlite3 "$snapshot" "
  SELECT m.name || '.' || p.name || '|' ||
    CASE WHEN p.\"notnull\" = 1 OR p.pk > 0 THEN 'NO' ELSE 'YES' END
  FROM sqlite_master m, pragma_table_info(m.name) p
  WHERE m.type = 'table' AND m.name NOT GLOB 'sqlite_*'
  ORDER BY 1;")
actual_column_contract=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
  "SELECT table_name || '.' || column_name || '|' || is_nullable
   FROM information_schema.columns
   WHERE table_schema = 'public'
   ORDER BY 1")
if [[ $actual_column_contract != "$expected_column_contract" ]]; then
  echo "PostgreSQL column/nullability schema does not match the SQLite source contract" >&2
  exit 1
fi

expected_primary_keys=$(sqlite3 "$snapshot" "
  SELECT m.name || '|' || p.pk || '|' || p.name
  FROM sqlite_master m, pragma_table_info(m.name) p
  WHERE m.type = 'table' AND m.name NOT GLOB 'sqlite_*' AND p.pk > 0
  ORDER BY m.name, p.pk;")
actual_primary_keys=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
  "SELECT tc.table_name || '|' || kcu.ordinal_position || '|' || kcu.column_name
   FROM information_schema.table_constraints tc
   JOIN information_schema.key_column_usage kcu
     ON kcu.constraint_schema = tc.constraint_schema
       AND kcu.constraint_name = tc.constraint_name
       AND kcu.table_schema = tc.table_schema
       AND kcu.table_name = tc.table_name
   WHERE tc.constraint_schema = 'public' AND tc.constraint_type = 'PRIMARY KEY'
   ORDER BY tc.table_name, kcu.ordinal_position")
if [[ $actual_primary_keys != "$expected_primary_keys" ]]; then
  echo "PostgreSQL primary-key schema does not match the SQLite source contract" >&2
  exit 1
fi

for definition in "${boolean_columns[@]}"; do
  IFS='|' read -r qualified _ target_default nullable <<<"$definition"
  table=${qualified%%.*}
  column=${qualified#*.}
  actual=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT data_type || '|' || coalesce(column_default, '<NULL>') || '|' || is_nullable
     FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = '$table' AND column_name = '$column'")
  if [[ $actual != "boolean|$target_default|$nullable" ]]; then
    echo "PostgreSQL boolean schema does not match the reviewed contract: $qualified" >&2
    exit 1
  fi
done
for definition in "${json_columns[@]}"; do
  IFS='|' read -r qualified nullable <<<"$definition"
  table=${qualified%%.*}
  column=${qualified#*.}
  actual=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT data_type || '|' || coalesce(column_default, '<NULL>') || '|' || is_nullable
     FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = '$table' AND column_name = '$column'")
  if [[ $actual != "json|<NULL>|$nullable" ]]; then
    echo "PostgreSQL JSON schema does not match the reviewed contract: $qualified" >&2
    exit 1
  fi
done

# pgloader normally resets sequences. Repeat that operation explicitly and
# verify it so a migrated maximum numeric ID cannot collide on the next write.
psql "$postgres_dsn" -v ON_ERROR_STOP=1 <<'SQL'
DO $$
DECLARE
  item record;
  sequence_name text;
  maximum_value bigint;
BEGIN
  FOR item IN
    SELECT table_schema, table_name, column_name
    FROM information_schema.columns
    WHERE table_schema = 'public'
    ORDER BY table_name, ordinal_position
  LOOP
    sequence_name := pg_get_serial_sequence(
      format('%I.%I', item.table_schema, item.table_name),
      item.column_name
    );
    IF sequence_name IS NULL THEN
      CONTINUE;
    END IF;
    EXECUTE format('SELECT max(%I) FROM %I.%I', item.column_name, item.table_schema, item.table_name)
      INTO maximum_value;
    IF maximum_value IS NULL THEN
      PERFORM setval(sequence_name, 1, false);
    ELSE
      PERFORM setval(sequence_name, maximum_value, true);
    END IF;
  END LOOP;
END
$$;
SQL

printf 'table\tsqlite_rows\tpostgres_rows\tsqlite_pk_sha256\tpostgres_pk_sha256\tstatus\n' >"$report"
mismatch=0

while IFS= read -r table; do
  [[ -z $table ]] && continue
  sqlite_ident=${table//\"/\"\"}
  sqlite_literal=${table//\'/\'\'}
  postgres_ident=$sqlite_ident

  sqlite_rows=$(sqlite3 "$snapshot" "SELECT count(*) FROM \"$sqlite_ident\";")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.\"$postgres_ident\";")

  mapfile -t pk_columns < <(sqlite3 "$snapshot" "SELECT name FROM pragma_table_info('$sqlite_literal') WHERE pk > 0 ORDER BY pk;")
  sqlite_pk=-
  postgres_pk=-
  if (( ${#pk_columns[@]} > 0 )); then
    sqlite_expr=''
    postgres_expr=''
    order_by=''
    for column in "${pk_columns[@]}"; do
      column_ident=${column//\"/\"\"}
      if [[ -n $sqlite_expr ]]; then
        sqlite_expr+=' || char(31) || '
        postgres_expr+=' || chr(31) || '
        order_by+=', '
      fi
      sqlite_expr+="COALESCE(CAST(\"$column_ident\" AS TEXT), '<NULL>')"
      postgres_expr+="COALESCE(CAST(\"$column_ident\" AS TEXT), '<NULL>')"
      order_by+="\"$column_ident\""
    done
    sqlite_pk=$(sqlite3 "$snapshot" "SELECT $sqlite_expr FROM \"$sqlite_ident\" ORDER BY $order_by;" | LC_ALL=C sha256sum | cut -d' ' -f1)
    postgres_pk=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc "SELECT $postgres_expr FROM public.\"$postgres_ident\" ORDER BY $order_by;" | LC_ALL=C sha256sum | cut -d' ' -f1)
  fi

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_pk != "$postgres_pk" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$table" "$sqlite_rows" "$postgres_rows" "$sqlite_pk" "$postgres_pk" "$status" >>"$report"
done < <(sqlite3 "$snapshot" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*' ORDER BY name;")

verify_blob_set() {
  local table=$1
  local id_column=$2
  local blob_column=$3
  local sqlite_rows postgres_rows sqlite_digest postgres_digest status

  sqlite_rows=$(sqlite3 "$snapshot" "SELECT count(*) FROM \"$table\" WHERE length(\"$blob_column\") > 0;")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT count(*) FROM public.\"$table\" WHERE octet_length(\"$blob_column\") > 0;")
  sqlite_digest=$(sqlite3 "$snapshot" \
    "SELECT CAST(\"$id_column\" AS TEXT) || char(31) || lower(hex(\"$blob_column\")) FROM \"$table\" WHERE length(\"$blob_column\") > 0 ORDER BY \"$id_column\";" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)
  postgres_digest=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT CAST(\"$id_column\" AS TEXT) || chr(31) || encode(\"$blob_column\", 'hex') FROM public.\"$table\" WHERE octet_length(\"$blob_column\") > 0 ORDER BY \"$id_column\";" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_digest != "$postgres_digest" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@blob_%s_%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$table" "$blob_column" "$sqlite_rows" "$postgres_rows" "$sqlite_digest" "$postgres_digest" "$status" >>"$report"
}

verify_blob_set mujian_image_generations id result_data
verify_blob_set mujian_image_references id data

verify_boolean_set() {
  local qualified=$1
  local table=${qualified%%.*}
  local column=${qualified#*.}
  local sqlite_rows postgres_rows sqlite_expr='' postgres_expr='' order_by=''
  local pk_column pk_ident
  local sqlite_digest postgres_digest status
  local -a pk_columns

  sqlite_rows=$(sqlite3 "$snapshot" "SELECT count(*) FROM \"$table\";")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT count(*) FROM public.\"$table\";")
  mapfile -t pk_columns < <(sqlite3 "$snapshot" \
    "SELECT name FROM pragma_table_info('$table') WHERE pk > 0 ORDER BY pk;")
  if (( ${#pk_columns[@]} == 0 )); then
    echo "cannot verify boolean values without a primary key: $qualified" >&2
    exit 1
  fi
  for pk_column in "${pk_columns[@]}"; do
    pk_ident=${pk_column//\"/\"\"}
    if [[ -n $sqlite_expr ]]; then
      sqlite_expr+=' || char(31) || '
      postgres_expr+=' || chr(31) || '
      order_by+=', '
    fi
    sqlite_expr+="COALESCE(CAST(\"$pk_ident\" AS TEXT), '<NULL>')"
    postgres_expr+="COALESCE(CAST(\"$pk_ident\" AS TEXT), '<NULL>')"
    order_by+="\"$pk_ident\""
  done
  sqlite_expr+=" || char(30) || CASE WHEN \"$column\" IS NULL THEN '-' WHEN \"$column\" = 0 THEN '0' ELSE '1' END"
  postgres_expr+=" || chr(30) || CASE WHEN \"$column\" IS NULL THEN '-' WHEN \"$column\" IS false THEN '0' ELSE '1' END"
  sqlite_digest=$(sqlite3 "$snapshot" \
    "SELECT $sqlite_expr FROM \"$table\" ORDER BY $order_by;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)
  postgres_digest=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT $postgres_expr FROM public.\"$table\" ORDER BY $order_by;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_digest != "$postgres_digest" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@boolean_%s_%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$table" "$column" "$sqlite_rows" "$postgres_rows" \
    "$sqlite_digest" "$postgres_digest" "$status" >>"$report"
}

verify_json_set() {
  local qualified=$1
  local table=${qualified%%.*}
  local column=${qualified#*.}
  local sqlite_rows postgres_rows sqlite_digest postgres_digest status

  sqlite_rows=$(sqlite3 "$snapshot" \
    "SELECT count(*) FROM \"$table\" WHERE \"$column\" IS NOT NULL;")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT count(*) FROM public.\"$table\" WHERE \"$column\" IS NOT NULL;")
  sqlite_digest=$(sqlite3 "$snapshot" \
    "SELECT CAST(id AS TEXT) || char(31) || lower(hex(\"$column\"))
     FROM \"$table\" WHERE \"$column\" IS NOT NULL ORDER BY id;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)
  postgres_digest=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT CAST(id AS TEXT) || chr(31) || encode(convert_to(CAST(\"$column\" AS TEXT), 'UTF8'), 'hex')
     FROM public.\"$table\" WHERE \"$column\" IS NOT NULL ORDER BY id;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_digest != "$postgres_digest" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@json_%s_%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$table" "$column" "$sqlite_rows" "$postgres_rows" \
    "$sqlite_digest" "$postgres_digest" "$status" >>"$report"
}

for definition in "${boolean_columns[@]}"; do
  verify_boolean_set "${definition%%|*}"
done
for definition in "${json_columns[@]}"; do
  verify_json_set "${definition%%|*}"
done

expected_users=${EXPECTED_USERS:-8}
expected_channels=${EXPECTED_CHANNELS:-10}
actual_users=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc 'SELECT count(*) FROM public.users')
actual_channels=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc 'SELECT count(*) FROM public.channels')
for expected_check in "users:$expected_users:$actual_users" "channels:$expected_channels:$actual_channels"; do
  IFS=: read -r check_name expected_count actual_count <<<"$expected_check"
  status=ok
  if [[ $expected_count != "$actual_count" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@expected_%s\t%s\t%s\t-\t-\t%s\n' \
    "$check_name" "$expected_count" "$actual_count" "$status" >>"$report"
done

# Sensitive values never enter the report. Each row is encoded with explicit
# NULL markers, the encoded rows are sorted, and only the aggregate SHA-256 is
# retained. bool: normalizes 0/1 vs false/true; present: compares nullable
# state markers without depending on SQLite/PostgreSQL timestamp formatting.
build_sensitive_expression() {
  local dialect=$1
  shift
  local expression='' part='' encoded_value spec column

  for spec in "$@"; do
    case $spec in
      bool:* | present:*) column=${spec#*:} ;;
      *) column=$spec ;;
    esac
    if [[ ! $column =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "invalid sensitive-set column: $column" >&2
      exit 1
    fi

    if [[ $spec == bool:* ]]; then
      if [[ $dialect == sqlite ]]; then
        encoded_value="lower(hex(CAST(\"$column\" AS BLOB)))"
      else
        encoded_value="encode(convert_to(CAST(\"$column\" AS TEXT), 'UTF8'), 'hex')"
      fi
      part="CASE
        WHEN \"$column\" IS NULL THEN '-'
        WHEN lower(CAST(\"$column\" AS TEXT)) IN ('1', 't', 'true') THEN '+1'
        WHEN lower(CAST(\"$column\" AS TEXT)) IN ('0', 'f', 'false') THEN '+0'
        ELSE '!invalid-boolean:' || $encoded_value
      END"
    elif [[ $spec == present:* ]]; then
      part="CASE WHEN \"$column\" IS NULL THEN '-0' ELSE '+1' END"
    elif [[ $dialect == sqlite ]]; then
      part="CASE WHEN \"$column\" IS NULL THEN '-'
        ELSE '+' || lower(hex(CAST(\"$column\" AS BLOB))) END"
    else
      part="CASE WHEN \"$column\" IS NULL THEN '-'
        ELSE '+' || encode(convert_to(CAST(\"$column\" AS TEXT), 'UTF8'), 'hex') END"
    fi

    if [[ -n $expression ]]; then
      expression+=" || '|' || "
    fi
    expression+=$part
  done
  printf '%s' "$expression"
}

verify_sensitive_set() {
  local label=$1
  local table=$2
  shift 2
  local sqlite_expression postgres_expression
  local sqlite_rows postgres_rows sqlite_digest postgres_digest status

  if [[ ! $label =~ ^[A-Za-z0-9_]+$ || ! $table =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
    echo "invalid sensitive-set label or table" >&2
    exit 1
  fi
  sqlite_expression=$(build_sensitive_expression sqlite "$@")
  postgres_expression=$(build_sensitive_expression postgres "$@")

  sqlite_rows=$(sqlite3 "$snapshot" "SELECT count(*) FROM \"$table\";")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT count(*) FROM public.\"$table\";")
  sqlite_digest=$(sqlite3 "$snapshot" \
    "SELECT payload FROM (SELECT $sqlite_expression AS payload FROM \"$table\") ORDER BY payload COLLATE BINARY;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)
  postgres_digest=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT payload FROM (SELECT $postgres_expression AS payload FROM public.\"$table\") sensitive_rows ORDER BY payload COLLATE \"C\";" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_digest != "$postgres_digest" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@sensitive_%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$label" "$sqlite_rows" "$postgres_rows" "$sqlite_digest" "$postgres_digest" "$status" >>"$report"
}

task_private_json='c."private_data"::jsonb'

verify_task_private_keys() {
  local sqlite_json='CAST(c."private_data" AS TEXT)'
  local sqlite_rows postgres_rows sqlite_digest postgres_digest status

  sqlite_rows=$(sqlite3 "$snapshot" \
    "SELECT count(*) FROM tasks c WHERE json_extract($sqlite_json, '$.key') IS NOT NULL;")
  postgres_rows=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT count(*) FROM public.tasks c WHERE (($task_private_json) ->> 'key') IS NOT NULL;")
  sqlite_digest=$(sqlite3 "$snapshot" \
    "SELECT payload FROM (
       SELECT CAST(c.id AS TEXT) || char(31) ||
         lower(hex(CAST(json_extract($sqlite_json, '$.key') AS BLOB))) AS payload
       FROM tasks c WHERE json_extract($sqlite_json, '$.key') IS NOT NULL
     ) ORDER BY payload COLLATE BINARY;" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)
  postgres_digest=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc \
    "SELECT payload FROM (
       SELECT CAST(c.id AS TEXT) || chr(31) ||
         encode(convert_to(($task_private_json) ->> 'key', 'UTF8'), 'hex') AS payload
       FROM public.tasks c WHERE (($task_private_json) ->> 'key') IS NOT NULL
     ) sensitive_rows ORDER BY payload COLLATE \"C\";" \
    | LC_ALL=C sha256sum | cut -d' ' -f1)

  status=ok
  if [[ $sqlite_rows != "$postgres_rows" || $sqlite_digest != "$postgres_digest" ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@sensitive_task_private_keys\t%s\t%s\t%s\t%s\t%s\n' \
    "$sqlite_rows" "$postgres_rows" "$sqlite_digest" "$postgres_digest" "$status" >>"$report"
}

verify_sensitive_set channels channels \
  id key open_ai_organization base_url other models model_mapping status_code_mapping \
  other_info setting param_override header_override settings
verify_sensitive_set users users \
  id username password role status email github_id discord_id oidc_id wechat_id telegram_id \
  access_token group aff_code linux_do_id setting stripe_customer present:deleted_at
verify_sensitive_set tokens tokens \
  id user_id key status expired_time remain_quota bool:unlimited_quota \
  bool:model_limits_enabled model_limits allow_ips group bool:cross_group_retry present:deleted_at
verify_sensitive_set options options key value
verify_sensitive_set custom_oauth custom_oauth_providers \
  id slug bool:enabled client_id client_secret authorization_endpoint token_endpoint \
  user_info_endpoint scopes user_id_field username_field display_name_field email_field \
  well_known auth_style access_policy access_denied_message
verify_sensitive_set two_fa two_fas \
  id user_id secret bool:is_enabled failed_attempts present:locked_until present:deleted_at
verify_sensitive_set two_fa_backup two_fa_backup_codes \
  id user_id code_hash bool:is_used present:deleted_at
verify_sensitive_set passkeys passkey_credentials \
  id user_id credential_id public_key attestation_type aa_guid sign_count \
  bool:clone_warning bool:user_present bool:user_verified bool:backup_eligible \
  bool:backup_state transports attachment present:deleted_at
verify_sensitive_set oauth_bindings user_oauth_bindings id user_id provider_id provider_user_id
verify_sensitive_set redemptions redemptions \
  id user_id key status used_user_id expired_time present:deleted_at
verify_sensitive_set subscription_provider_payload subscription_orders \
  id user_id plan_id trade_no payment_method status provider_payload
verify_task_private_keys

verify_orphans() {
  local name=$1
  local query=$2
  local orphan_count
  orphan_count=$(psql "$postgres_dsn" -v ON_ERROR_STOP=1 -Atc "$query")
  local status=ok
  if [[ $orphan_count != 0 ]]; then
    status=mismatch
    mismatch=1
  fi
  printf '@orphans_%s\t0\t%s\t-\t-\t%s\n' "$name" "$orphan_count" "$status" >>"$report"
}

verify_orphans ability_channel 'SELECT count(*) FROM public.abilities c LEFT JOIN public.channels p ON p.id=c.channel_id WHERE p.id IS NULL'
verify_orphans price_channel 'SELECT count(*) FROM public.channel_model_prices c LEFT JOIN public.channels p ON p.id=c.channel_id WHERE p.id IS NULL'
verify_orphans checkin_user 'SELECT count(*) FROM public.checkins c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans log_user 'SELECT count(*) FROM public.logs c LEFT JOIN public.users p ON p.id=c.user_id WHERE c.user_id <> 0 AND p.id IS NULL'
verify_orphans log_channel 'SELECT count(*) FROM public.logs c LEFT JOIN public.channels p ON p.id=c.channel_id WHERE c.channel_id <> 0 AND p.id IS NULL'
verify_orphans log_token 'SELECT count(*) FROM public.logs c LEFT JOIN public.tokens p ON p.id=c.token_id WHERE c.token_id <> 0 AND p.id IS NULL'
verify_orphans midjourney_user 'SELECT count(*) FROM public.midjourneys c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans midjourney_channel 'SELECT count(*) FROM public.midjourneys c LEFT JOIN public.channels p ON p.id=c.channel_id WHERE p.id IS NULL'
verify_orphans model_vendor 'SELECT count(*) FROM public.models c LEFT JOIN public.vendors p ON p.id=c.vendor_id WHERE c.vendor_id IS NOT NULL AND c.vendor_id <> 0 AND p.id IS NULL'
verify_orphans project_user 'SELECT count(*) FROM public.mujian_projects c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans scene_project 'SELECT count(*) FROM public.mujian_scenes c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans shot_project 'SELECT count(*) FROM public.mujian_shots c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans shot_scene 'SELECT count(*) FROM public.mujian_shots c LEFT JOIN public.mujian_scenes p ON p.id=c.scene_id WHERE p.id IS NULL'
verify_orphans session_project 'SELECT count(*) FROM public.mujian_agent_sessions c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans session_user 'SELECT count(*) FROM public.mujian_agent_sessions c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans message_project 'SELECT count(*) FROM public.mujian_agent_messages c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans message_user 'SELECT count(*) FROM public.mujian_agent_messages c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans message_session "SELECT count(*) FROM public.mujian_agent_messages c LEFT JOIN public.mujian_agent_sessions p ON p.id=c.session_id WHERE c.session_id <> '' AND p.id IS NULL"
verify_orphans generation_project 'SELECT count(*) FROM public.mujian_image_generations c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans generation_user 'SELECT count(*) FROM public.mujian_image_generations c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans generation_session "SELECT count(*) FROM public.mujian_image_generations c LEFT JOIN public.mujian_agent_sessions p ON p.id=c.session_id WHERE c.session_id <> '' AND p.id IS NULL"
# shot_id is historical provenance, while task_id and generation_task_id are
# correlation IDs whose task rows may be purged after completion. They are not
# durable ownership FKs and therefore are intentionally excluded here.
verify_orphans reference_generation 'SELECT count(*) FROM public.mujian_image_references c LEFT JOIN public.mujian_image_generations p ON p.id=c.generation_id WHERE p.id IS NULL'
verify_orphans seedance_project 'SELECT count(*) FROM public.mujian_seedance_shots c LEFT JOIN public.mujian_projects p ON p.id=c.project_id WHERE p.id IS NULL'
verify_orphans seedance_user 'SELECT count(*) FROM public.mujian_seedance_shots c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans seedance_session 'SELECT count(*) FROM public.mujian_seedance_shots c LEFT JOIN public.mujian_agent_sessions p ON p.id=c.session_id WHERE p.id IS NULL'
verify_orphans seedance_first_frame "SELECT count(*) FROM public.mujian_seedance_shots c LEFT JOIN public.mujian_image_generations p ON p.id=c.first_frame_generation_id WHERE c.first_frame_generation_id IS NOT NULL AND c.first_frame_generation_id <> '' AND p.id IS NULL"
verify_orphans preference_user 'SELECT count(*) FROM public.mujian_user_preferences c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans passkey_user 'SELECT count(*) FROM public.passkey_credentials c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans quota_user 'SELECT count(*) FROM public.quota_data c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans redemption_creator 'SELECT count(*) FROM public.redemptions c LEFT JOIN public.users p ON p.id=c.user_id WHERE c.user_id <> 0 AND p.id IS NULL'
verify_orphans redemption_user 'SELECT count(*) FROM public.redemptions c LEFT JOIN public.users p ON p.id=c.used_user_id WHERE c.used_user_id <> 0 AND p.id IS NULL'
verify_orphans order_user 'SELECT count(*) FROM public.subscription_orders c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans order_plan 'SELECT count(*) FROM public.subscription_orders c LEFT JOIN public.subscription_plans p ON p.id=c.plan_id WHERE p.id IS NULL'
verify_orphans subscription_user 'SELECT count(*) FROM public.user_subscriptions c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans subscription_plan 'SELECT count(*) FROM public.user_subscriptions c LEFT JOIN public.subscription_plans p ON p.id=c.plan_id WHERE p.id IS NULL'
verify_orphans preconsume_user 'SELECT count(*) FROM public.subscription_pre_consume_records c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans preconsume_subscription 'SELECT count(*) FROM public.subscription_pre_consume_records c LEFT JOIN public.user_subscriptions p ON p.id=c.user_subscription_id WHERE p.id IS NULL'
verify_orphans token_user 'SELECT count(*) FROM public.tokens c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans task_user 'SELECT count(*) FROM public.tasks c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans task_channel 'SELECT count(*) FROM public.tasks c LEFT JOIN public.channels p ON p.id=c.channel_id WHERE c.channel_id <> 0 AND p.id IS NULL'
verify_orphans topup_user 'SELECT count(*) FROM public.top_ups c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans two_fa_user 'SELECT count(*) FROM public.two_fas c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans two_fa_backup_user 'SELECT count(*) FROM public.two_fa_backup_codes c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans oauth_binding_user 'SELECT count(*) FROM public.user_oauth_bindings c LEFT JOIN public.users p ON p.id=c.user_id WHERE p.id IS NULL'
verify_orphans oauth_binding_provider 'SELECT count(*) FROM public.user_oauth_bindings c LEFT JOIN public.custom_oauth_providers p ON p.id=c.provider_id WHERE p.id IS NULL'
verify_orphans user_inviter 'SELECT count(*) FROM public.users c LEFT JOIN public.users p ON p.id=c.inviter_id WHERE c.inviter_id IS NOT NULL AND c.inviter_id <> 0 AND p.id IS NULL'

printf '# mode=%s target_database=%s snapshot=%s snapshot_sha256=%s\n' \
  "$mode" "$target_database" "$snapshot" "$(cut -d' ' -f1 "$snapshot.sha256")" >>"$report"

if (( mismatch != 0 )); then
  echo "database migration verification failed; see $report" >&2
  exit 1
fi

echo "pgloader migration verified"
echo "snapshot: $snapshot"
echo "report: $report"
echo "next: start the application once to run additive AutoMigrate, then run the image migration verification"
