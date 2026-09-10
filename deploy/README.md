# Mujian production rollout

The application listens only on `127.0.0.1:10088`; Nginx owns the public
HTTPS endpoint. PostgreSQL and Redis have no host port mapping. Keep the main
site DNS unchanged until the rehearsal, final database/image migration, and
loopback application checks below have passed. The container disables its
unbounded `/data/logs` file copy; stdout/stderr are retained by Docker's
size- and count-limited `json-file` rotation.

Cloud mutations are separate change-control gates. Obtain explicit approval
before making the GitHub repository public, attaching an ECS agency, changing
the OBS public-read policy/CORS/custom domain, changing security groups or DNS,
or purchasing a CCM certificate. A real image-generation request can incur
model charges and requires a second, separate approval.

## 1. Prepare and build without routing traffic

Run `deploy/scripts/install-host-dependencies.sh` as root. Put the exact clean,
tagged source tree at `/opt/mujian/releases/<commit-sha>`. Copy
`.env.mujian.example` to `deploy/runtime/mujian.env`, replace every placeholder,
and set its mode to `0600`. In production, `POSTGRES_DB` and
`MUJIAN_DATABASE_NAME` must both be `mujian`; the latter exists so rehearsal
commands can target a different database without changing the PostgreSQL
service's initialization database or health check.

Build before publishing or starting the application:

```sh
deploy/scripts/deploy-release.sh build
```

The build uses a candidate tag, compares its content digest with any existing
full-SHA tag, and refuses to replace a different image already carrying that
SHA. It verifies the OCI source/revision/version labels and writes a mode-0600
artifact to `deploy/reports/release-<commit-sha>.txt`. Preserve that artifact.

## 2. Configure OBS prerequisites after approval

Before any image rehearsal, attach the approved least-privilege ECS agency and
configure the private `mujianai` bucket as documented in `deploy/obs/README.md`.
Record all of the following as rollout evidence:

- bucket versioning is **Disabled / never enabled**; `Enabled` and `Suspended`
  are both release blockers because overwrite-forbid semantics do not protect
  an object key in a versioned bucket;
- only `mujian/prod/public/*` has anonymous `GetObject` permission and a probe
  outside that prefix returns `403`;
- CORS permits only `https://www.mujianai.com` for `GET` and `HEAD`;
- `static.mujianai.com` is bound to OBS with the approved certificate;
- an ECS-agency request can Put/Get/Head/Delete only within the public prefix.

Do not store an AK/SK in Git, the runtime env file, Compose, an image layer, or
logs.

## 3. Rehearse SQLite, PostgreSQL, OBS, and the local app

Use the online SQLite backup as the rehearsal source. The migration script
accepts the pinned Ubuntu package `pgloader=3.6.10-1build2` only. That package's
binary still reports the upstream build string `3.6.7~devel`, so the gate checks
dpkg ownership and the exact package version rather than trusting the display
string. It resets a database only when both destructive confirmations are exact.

```sh
export SQLITE_DB=/absolute/path/one-api.db
export MIGRATION_RESET_TARGET=YES
export MIGRATION_CONFIRM_DATABASE=mujian_rehearsal
deploy/scripts/sqlite-to-postgres.sh rehearsal
```

The script starts only Compose PostgreSQL, leaves its configured `POSTGRES_DB`
as `mujian`, creates `mujian_rehearsal`, and supplies the password through a
temporary mode-0600 `PGPASSFILE`. Port 5432 remains unpublished. An explicitly
supplied `POSTGRES_DSN` must omit an inline password and use `PGPASSFILE`.

Run the application schema migration before image commands. Override only
`MUJIAN_DATABASE_NAME`, so the app/CLI connects to the rehearsal database while
the PostgreSQL service configuration remains unchanged:

```sh
umask 077
install -d -m 0700 deploy/reports

MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  schema --expect-database mujian_rehearsal --report - \
  > deploy/reports/rehearsal-schema.json

MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  images-to-obs --expect-database mujian_rehearsal --report - \
  > deploy/reports/rehearsal-images-dry-run.json

MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  images-to-obs --apply --expect-database mujian_rehearsal --report - \
  > deploy/reports/rehearsal-images.json

MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  verify --expect-database mujian_rehearsal --report - \
  > deploy/reports/rehearsal-images-verify.json
```

Start the exact image against the rehearsal database on loopback only. This is
not the production `up` action and does not require DNS:

```sh
MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  up -d --no-build --pull never --wait --wait-timeout 120 postgres redis app
curl --fail --silent http://127.0.0.1:10088/api/status
MUJIAN_DATABASE_NAME=mujian_rehearsal \
  docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  stop app
```

Also HEAD the rehearsal's public OBS objects through `static.mujianai.com` and
restart the loopback app once. Production cookies are intentionally Secure, so
the complete browser login/project flow is deferred until real TLS is enabled;
do not weaken the cookie for rehearsal. Do not perform a paid generation in
this phase without its separate approval. The TSV SQLite report and JSON OBS
reports must agree before continuing. `source_404` for the two known missing
legacy shot URLs is accepted; other mismatches are blockers.

## 4. Final cutover while main DNS is still unchanged

Stop the original local Go service first and confirm it cannot accept writes.
Then take the final consistent snapshot and replace only the production target:

```sh
export SQLITE_DB=/absolute/path/one-api.db
export MIGRATION_RESET_TARGET=YES
export MIGRATION_CONFIRM_DATABASE=mujian
export MIGRATION_SOURCE_STOPPED=YES
deploy/scripts/sqlite-to-postgres.sh cutover
```

Run schema migration, idempotent OBS backfill, verification, and inventory
against the explicitly guarded production database:

```sh
umask 077
install -d -m 0700 deploy/reports deploy/backups

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  schema --expect-database mujian --report - \
  > deploy/reports/production-schema.json

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  images-to-obs --expect-database mujian --report - \
  > deploy/reports/production-images-dry-run.json

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  images-to-obs --apply --expect-database mujian --report - \
  > deploy/reports/production-images.json

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  verify --expect-database mujian --report - \
  > deploy/reports/production-images-verify.json

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  run --rm --no-deps --entrypoint /mujian-migrate app \
  report --expect-database mujian --report - \
  > deploy/reports/production-object-inventory.json
```

Create and checksum the rollback PostgreSQL dump without placing its password
in a command argument:

```sh
backup_file="deploy/backups/mujian-$(date -u +%Y%m%dT%H%M%SZ).dump"
docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  exec -T postgres sh -c \
  'exec pg_dump --format=custom --no-owner --no-acl --username="$POSTGRES_USER" --dbname="$POSTGRES_DB"' \
  > "$backup_file"
chmod 0400 "$backup_file"
sha256sum "$backup_file" > "$backup_file.sha256"
```

After separate approval, publish `main` and `v0.12.14-obs.1` at the exact SHA
and make the source repository public. Production start refuses private or
mismatched GitHub main/tag/SHA state, refuses an unrecorded image digest, and
never builds or pulls implicitly:

```sh
deploy/scripts/deploy-release.sh up
curl --fail --silent http://127.0.0.1:10088/api/status
```

Restart the loopback app once. Verify PostgreSQL
row/primary-key/orphan/sensitive hashes and OBS HEAD/size/SHA results before
touching main-site DNS. The complete authenticated browser path still waits for
real TLS so the Secure-cookie policy is exercised unchanged.

## 5. Enable DNS, HTTPS, and traffic last

After explicit approval, make security-group changes so only SSH and public
HTTP/HTTPS are permitted; ports 10088, 5432, and 6379 must remain unreachable
from the Internet. Install the temporary HTTP configuration, then change the
`mujianai.com` and `www.mujianai.com` A records to the ECS:

```sh
sudo deploy/scripts/configure-nginx-http.sh
```

Wait for public DNS resolution, issue the certificate, and enable the final
proxy only after the local application remains healthy:

```sh
sudo certbot certonly --webroot -w /var/www/certbot \
  --cert-name www.mujianai.com \
  -d www.mujianai.com -d mujianai.com
sudo deploy/scripts/configure-nginx.sh
sudo systemctl is-enabled certbot.timer
sudo systemctl is-active certbot.timer
```

`configure-nginx.sh` installs the deploy-time Nginx reload hook, enables and
starts `certbot.timer`, and requires `certbot renew --dry-run` to pass. It also
validates Nginx before every reload.

Run strict acceptance from a machine outside the ECS. All three variables are
mandatory; the script does not silently skip login/Secure-cookie or OBS checks.
Use a test account without 2FA and read the password without echoing it:

```sh
read -r -p 'Test username: ' MUJIAN_TEST_USERNAME
read -r -s -p 'Test password: ' MUJIAN_TEST_PASSWORD
printf '\n'
read -r -p 'New public OBS image URL: ' MUJIAN_TEST_IMAGE_URL
export MUJIAN_TEST_USERNAME MUJIAN_TEST_PASSWORD MUJIAN_TEST_IMAGE_URL
deploy/scripts/verify-production.sh
unset MUJIAN_TEST_PASSWORD
```

This verifies HTTPS/HSTS, the HTTP redirect, certificate host/validity, Secure
session cookie attributes, anonymous public-image MIME/disposition/cache/CORS,
private-prefix `403`, and closed public ports. Next, in a real browser, log in,
open a migrated project, create a test project, upload a reference, verify its
OBS preview/download across an app restart, then delete the test project and
confirm object cleanup. A paid generation and its full UI
poll/preview/download/regenerate/delete lifecycle remain a separately approved
final check.

## 5.1 Canary the managed Claude providers

Do not place either upstream API key in source code, shell history, migration
reports, or logs. Enter the keys only in the Root provider page. The binary
default for `RetryTimes` remains `0`; do not persist `1` until the no-retry
provider checks in the canary group have passed.

Before changing traffic, create executable, mode-0600 restore files for the two
managed channel rows and the current persisted `RetryTimes` state. The channel
artifact restores the captured non-secret configuration but always forces both
managed channels to `ChannelStatusManuallyDisabled` (`status=2`) and their
abilities disabled, including a provider tag that
did not exist at snapshot time. It deliberately excludes `channels.key`, so it
is safe to retain and cannot disclose either provider credential:

```sh
set -eu
umask 077
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
install -d -m 0700 "$report_dir"
retry_before="$(
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At' <<'SQL'
SELECT COALESCE((SELECT value FROM public.options WHERE key = 'RetryTimes'), '0');
SQL
)"
test "$retry_before" = 0 || {
  echo "RetryTimes must be 0 before canary (got $retry_before)" >&2
  exit 1
}

{
  printf 'BEGIN;\n'
  printf '%s\n' \
    "UPDATE public.channels SET status=2 WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat');" \
    "UPDATE public.abilities SET enabled=false WHERE channel_id IN (SELECT id FROM public.channels WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat'));"
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -qAt' <<'SQL'
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT format(
  'DO $mujian_restore$ BEGIN UPDATE public.channels SET status=2, "group"=%L, priority=%s, weight=%s, models=%L, model_mapping=%L, base_url=%L, test_model=%L WHERE id=%s AND tag=%L; IF NOT FOUND THEN RAISE EXCEPTION ''snapshotted managed channel is missing''; END IF; END $mujian_restore$;',
  "group", COALESCE(priority::text, 'NULL'), COALESCE(weight::text, 'NULL'),
  models, model_mapping, base_url, test_model, id, tag
)
FROM public.channels
WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat')
ORDER BY id;
SELECT format('DELETE FROM public.abilities WHERE channel_id=%s;', id)
FROM public.channels
WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat')
ORDER BY id;
SELECT format(
  'INSERT INTO public.abilities ("group", model, channel_id, enabled, priority, weight, tag) VALUES (%L, %L, %s, false, %s, %s, %L);',
  abilities."group", abilities.model, abilities.channel_id,
  COALESCE(abilities.priority::text, 'NULL'), COALESCE(abilities.weight::text, 'NULL'),
  abilities.tag
)
FROM public.abilities
JOIN public.channels ON channels.id = abilities.channel_id
WHERE channels.tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat')
ORDER BY abilities.channel_id, abilities.model, abilities."group";
COMMIT;
SQL
  printf 'COMMIT;\n'
} >"$report_dir/managed-claude-channels-restore.sql"

{
  printf 'BEGIN;\n'
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At' <<'SQL'
SELECT COALESCE(
  (SELECT format(
    'INSERT INTO public.options (key, value) VALUES (''RetryTimes'', %L) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;',
    value
  ) FROM public.options WHERE key = 'RetryTimes'),
  'INSERT INTO public.options (key, value) VALUES (''RetryTimes'', ''0'') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;'
);
SQL
  printf 'COMMIT;\n'
} >"$report_dir/retry-times-restore.sql"
printf '%s\n' "$retry_before" >"$report_dir/retry-times-before.txt"
chmod 0600 "$report_dir/managed-claude-channels-restore.sql" \
  "$report_dir/retry-times-restore.sql" "$report_dir/retry-times-before.txt"
```

Inspect both restore files before continuing. Neither file may contain an API
key or an `UPDATE ... key =` assignment. Keep `MUJIAN_DEPLOYMENT_ID` stable and
unique for this cluster: forward and rollback reports are bound to both that ID
and the PostgreSQL database name. The preference command is dry-run by default:

```sh
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml run --rm --no-deps \
  --entrypoint /mujian-migrate app \
  default-chat-model --expect-database mujian \
  --deployment-id mujian-production --report -
```

Create a temporary `mujian-canary` group. Move only the test user's
`users.group` to that group. The reserved `mujian-internal` token must keep an
empty `tokens.group`; it derives routing from the user and must never be migrated
to the canary group. Snapshot and update only the user row, then verify the token
invariant (exactly one reserved token with an empty group is required):

```sh
set -eu
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
test -d "$report_dir" || { echo 'missing report directory' >&2; exit 1; }
test_user_id=REPLACE_WITH_NUMERIC_USER_ID
case "$test_user_id" in ''|*[!0-9]*) echo 'test_user_id must be numeric' >&2; exit 1;; esac
{
  printf 'BEGIN;\n'
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At' <<SQL
SELECT format(
  'DO \$mujian_restore\$ BEGIN UPDATE public.users SET "group"=%L WHERE id=%s; IF NOT FOUND THEN RAISE EXCEPTION ''snapshotted canary user is missing''; END IF; END \$mujian_restore\$;',
  "group", id
)
FROM public.users WHERE id = $test_user_id;
SQL
  printf 'COMMIT;\n'
} >"$report_dir/canary-user-group-restore.sql"
chmod 0600 "$report_dir/canary-user-group-restore.sql"
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
  'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' <<SQL
DO \$mujian_canary\$
DECLARE
  moved_count integer;
  internal_count integer;
  empty_group_count integer;
BEGIN
  UPDATE public.users SET "group" = 'mujian-canary' WHERE id = $test_user_id;
  GET DIAGNOSTICS moved_count = ROW_COUNT;
  SELECT count(*), count(*) FILTER (WHERE COALESCE("group", '') = '')
  INTO internal_count, empty_group_count
  FROM public.tokens
  WHERE user_id = $test_user_id AND name = 'mujian-internal';
  IF moved_count <> 1 OR internal_count <> 1 OR empty_group_count <> 1 THEN
    RAISE EXCEPTION 'canary user/internal-token invariant failed: moved=%, tokens=%, empty_group=%',
      moved_count, internal_count, empty_group_count;
  END IF;
END
\$mujian_canary\$;
SQL
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T redis redis-cli DEL "user:$test_user_id" >/dev/null
```

Sign the test account out and back in so its session also carries the canary
group before exercising the workbench.

In the Root provider page, set each new provider's
routing group to `mujian-canary` when saving its key (or use the dedicated
"Save group" control while it remains disabled), then continue in this order:

1. Save the key.
2. Sync the model and price snapshot.
3. Run the authenticated connection test.
4. Inspect the exact model mapping and price entries.
5. Enable TabCode first, then ZenMux.

Keep ZenMux at priority `600` and TabCode at `550`. The complete primary/backup
path applies only to `claude-sonnet-4-6`; TabCode must not advertise another
Claude model. First exercise each provider independently with `RetryTimes=0`.
After those canary checks pass, persist exactly one pre-response retry:

```sh
set -eu
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
  'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' <<'SQL'
INSERT INTO public.options (key, value) VALUES ('RetryTimes', '1')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
SQL
```

Confirm the application has synchronized the option, then complete at least 10
streamed workbench turns, including one controlled ZenMux failure before the
first response byte, and observe authentication, model, usage, billing, credit,
and request logs for at least 30 minutes. A failure after stream output begins
must surface as an SSE error and must not replay the request.

After the canary passes, use the provider page's dedicated group control to move
TabCode and then ZenMux into `default`. Immediately execute the captured
`canary-user-group-restore.sql`, clear `user:<test_user_id>` from Redis, and
sign the test account in again; the internal token group remains empty. Keep
the restore file for rollback evidence:

```sh
set -eu
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
test -r "$report_dir/canary-user-group-restore.sql" || {
  echo 'missing canary user-group restore file' >&2
  exit 1
}
test_user_id=REPLACE_WITH_NUMERIC_USER_ID
case "$test_user_id" in ''|*[!0-9]*) echo 'test_user_id must be numeric' >&2; exit 1;; esac
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
  'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' \
  <"$report_dir/canary-user-group-restore.sql"
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T redis redis-cli DEL "user:$test_user_id" >/dev/null
```

Observe existing Claude traffic before setting `MUJIAN_DEFAULT_CHAT_MODEL` to
`claude-sonnet-4-6` in the production runtime environment and deploying the same
release. Startup fails if this value is not a registered model in the enabled
chat allowlist. Until the forward command commits, the assignment gate remains
closed and new Sonnet-default onboarding fails without writing a preference or
token. Run the forward command immediately after deploying the environment
change. It backfills only users whose preference is still the old global
default and whose locked current `users.group` can route Sonnet 4.6 through the
same channel, mapping, lifecycle, and price predicate used by relay:

```sh
set -eu
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
test -d "$report_dir" || { echo 'missing report directory' >&2; exit 1; }
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml run --rm --no-deps \
  --user "$(id -u):$(id -g)" --volume "$report_dir:/reports" \
  --entrypoint /mujian-migrate app \
  default-chat-model --apply --expect-database mujian \
  --deployment-id mujian-production \
  --report /reports/default-chat-model-applied.json
```

The host bind mount is required because the one-shot container is removed. Keep
the applied version 4 report as the exact forward user-ID/status snapshot and
rollback lineage anchor. Rows whose
user group cannot route Sonnet are reported as `skipped_unavailable` and are not
changed. Its
`operation_id` and `commit_digest` are paired with a commit marker written to
the existing `options` table in the same transaction as the preference changes.
Rollback rejects a report with no matching marker (including a report left by a
crash before commit), a modified report, or a marker already consumed by an
earlier rollback. A separate active-cutover marker prevents a second forward
migration from mixing its assignments into the first rollback. A transactional
assignment gate closes the onboarding race before rollback clears the bounded,
sharded ledger in the existing `options` table. The forward transaction first
locks users and preferences, then atomically creates all 64 version-2 ledger
shards bound to its `operation_id`, and only then opens the gate as
`open:<operation_id>`. The gate cannot remain open if the report write or
database transaction fails. An in-flight Sonnet onboarding
transaction either commits first (changing the ledger and aborting that
rollback preflight) or observes the closed gate and rolls back its preference.
Onboarding reads the user's actual group inside the user-lock transaction and
requires a live Sonnet route before writing. The ledger tracks migration
assignments and users onboarded while Sonnet is the runtime default. An explicit
chat-model selection removes that user's ledger entry in
the same transaction. This lets rollback include post-snapshot automatic users
without touching pre-existing or post-cutover explicit Sonnet choices. DeepSeek
and all other explicit user choices are intentionally unchanged.

For provider rollback, first restore the previous default-model environment and
deploy it, then restore only the captured preferences, set persisted
`RetryTimes=0`, disable both managed channels, and finally restore the old
application release:

```sh
set -eu
umask 077
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
test -d "$report_dir" || { echo 'missing report directory' >&2; exit 1; }
test -r "$report_dir/default-chat-model-applied.json" || {
  echo 'missing applied default-model snapshot' >&2
  exit 1
}
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml run --rm --no-deps \
  --user "$(id -u):$(id -g)" --volume "$report_dir:/reports" \
  --entrypoint /mujian-migrate app \
  rollback-default-chat-model \
  --apply \
  --snapshot /reports/default-chat-model-applied.json \
  --expect-database mujian \
  --deployment-id mujian-production \
  --report /reports/default-chat-model-rollback.json
```

The rollback command locks all automatic-assignment ledger shards, aborts if
they changed after planning, and clears the exact frozen set in the same
transaction as the preference restore. It leaves the assignment gate closed,
so a stale Sonnet-default instance cannot create a post-rollback automatic
assignment. A successful applied report is therefore the zero-residual
preflight for automatic Sonnet assignments; remaining Sonnet preferences are
explicit user choices. Verify the gate and ledger before restoring the retry and
non-secret channel state with the captured executable files:

```sh
set -eu
report_dir="$(pwd)/deploy/reports"
case "$report_dir" in /*) ;; *) echo 'report_dir must be absolute' >&2; exit 1;; esac
test -d "$report_dir" || { echo 'missing report directory' >&2; exit 1; }
cutover_operation_id="$(
  python3 - "$report_dir/default-chat-model-applied.json" <<'PY'
import json
import pathlib
import sys

report = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
operation_id = report.get("operation_id", "")
if not operation_id:
    raise SystemExit("applied default-model report has no operation_id")
print(operation_id)
PY
)"
assignment_state="$(
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At -v expected_operation_id="$1"' \
    sh "$cutover_operation_id" <<'SQL'
WITH ledgers AS (
  SELECT key, value::jsonb AS payload
  FROM public.options
  WHERE key ~ '^_mujian_default_chat_model_auto:sonnet-4-6:[0-9]{2}$'
)
SELECT COALESCE((SELECT value FROM public.options WHERE key = '_mujian_default_chat_model_auto:gate'), 'missing') || ':' ||
       count(*) || ':' ||
       count(*) FILTER (WHERE
         (payload ->> 'version')::integer IS DISTINCT FROM 2 OR
         payload ->> 'model' IS DISTINCT FROM 'claude-sonnet-4-6' OR
         payload ->> 'operation_id' IS DISTINCT FROM :'expected_operation_id' OR
         (payload ->> 'shard')::integer IS DISTINCT FROM substring(key from '([0-9]{2})$')::integer OR
         jsonb_typeof(payload -> 'user_ids') IS DISTINCT FROM 'array'
       ) || ':' ||
       COALESCE(sum(CASE WHEN jsonb_typeof(payload -> 'user_ids') = 'array'
                         THEN jsonb_array_length(payload -> 'user_ids') ELSE 0 END), 0)
FROM ledgers;
SQL
)"
test "$assignment_state" = 'closed:64:0:0' || {
  echo "automatic Sonnet assignments remain ($assignment_state)" >&2
  exit 1
}
test_user_id=REPLACE_WITH_NUMERIC_USER_ID
case "$test_user_id" in ''|*[!0-9]*) echo 'test_user_id must be numeric' >&2; exit 1;; esac
for artifact in retry-times-restore.sql managed-claude-channels-restore.sql canary-user-group-restore.sql retry-times-before.txt; do
  test -s "$report_dir/$artifact" || {
    echo "missing or empty $report_dir/$artifact" >&2
    exit 1
  }
done
retry_expected="$(cat "$report_dir/retry-times-before.txt")"
case "$retry_expected" in ''|*[!0-9]*)
  echo "invalid RetryTimes snapshot ($retry_expected)" >&2
  exit 1
;; esac
for restore_file in retry-times-restore.sql managed-claude-channels-restore.sql canary-user-group-restore.sql; do
  test -r "$report_dir/$restore_file" || {
    echo "missing $report_dir/$restore_file" >&2
    exit 1
  }
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1' \
    <"$report_dir/$restore_file"
done
retry_after="$(
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At' <<'SQL'
SELECT COALESCE((SELECT value FROM public.options WHERE key = 'RetryTimes'), '0');
SQL
)"
test "$retry_after" = "$retry_expected" || {
  echo "RetryTimes restore verification failed ($retry_after)" >&2
  exit 1
}
managed_disabled_state="$(
  docker compose --env-file deploy/runtime/mujian.env \
    -f docker-compose.mujian.yml exec -T postgres sh -eu -c \
    'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At' <<'SQL'
SELECT (SELECT count(*) FROM public.channels
        WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat') AND status <> 2) || ':' ||
       (SELECT count(*) FROM public.abilities
        WHERE channel_id IN (SELECT id FROM public.channels
          WHERE tag IN ('mujian-provider:zenmux:chat', 'mujian-provider:tabcode:chat'))
          AND enabled = true);
SQL
)"
test "$managed_disabled_state" = '0:0' || {
  echo "managed Claude routes are still enabled ($managed_disabled_state)" >&2
  exit 1
}
docker compose --env-file deploy/runtime/mujian.env \
  -f docker-compose.mujian.yml exec -T redis redis-cli DEL "user:$test_user_id" >/dev/null
```

The restore script also disables a managed channel created after an empty or
partial snapshot; keys remain only in the database and are never reconstructed
from an artifact. Do not reopen traffic between this direct database restore and
the application restart. Confirm persisted `RetryTimes` and the disabled channel
state have synchronized before restoring the old application release.

No database schema rollback is required. TabCode Kiro remains an upstream
stability risk, and an interrupted stream cannot be resumed automatically; the
user must start a new turn.

## 6. Exact rollback procedures

All rollback paths must use this OBS-aware compatibility release. Never start a
binary that does not understand object keys. Preserve the final SQLite snapshot
and SHA-256, PostgreSQL dump and SHA-256, image/object reports, release artifact,
and Docker image before routing traffic.

### Switch new writes back to the database

Keep the same full-SHA image and PostgreSQL database. Copy the runtime file and
change exactly one setting; the release script rejects any other storage value:

```sh
rollback_env=deploy/runtime/mujian-db-rollback.env
install -m 0600 deploy/runtime/mujian.env "$rollback_env"
python3 - "$rollback_env" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "MUJIAN_IMAGE_STORAGE=obs"
if text.count(needle) != 1:
    raise SystemExit("expected exactly one OBS storage setting")
path.write_text(text.replace(needle, "MUJIAN_IMAGE_STORAGE=db"), encoding="utf-8")
PY
MUJIAN_ENV_FILE="$rollback_env" deploy/scripts/deploy-release.sh rollback-db
curl --fail --silent http://127.0.0.1:10088/api/status
```

Legacy BLOBs remain intact in the first release. Existing OBS-key rows are still
readable, while new generated/reference images use database storage. Return to
OBS mode with the unchanged primary env file and
`deploy/scripts/deploy-release.sh up` after remediation.

### Restart the recorded image content digest

Read the exact image ID and SHA from the preserved release artifact. If the SHA
tag was removed, restore it. Replacing a tag that points elsewhere requires an
explicit confirmation equal to the release SHA:

```sh
release_sha=REPLACE_WITH_FULL_RELEASE_SHA
artifact="deploy/reports/release-$release_sha.txt"
recorded_id=$(sed -n 's/^image_content_digest=//p' "$artifact")
test -n "$recorded_id"
printf '%s\n' "$recorded_id" | grep -Eq '^sha256:[0-9a-f]{64}$'
docker image inspect "$recorded_id" >/dev/null
current_id=$(docker image inspect --format '{{.Id}}' "mujian-newapi:$release_sha" 2>/dev/null || true)
if test -n "$current_id" && test "$current_id" != "$recorded_id"; then
  test "${ROLLBACK_CONFIRM_IMAGE_TAG_REPLACE:-}" = "$release_sha"
fi
docker image tag "$recorded_id" "mujian-newapi:$release_sha"
deploy/scripts/deploy-release.sh up
```

### Restore PostgreSQL only after separate destructive approval

This path discards the current production database. Confirm the exact backup
checksum and database name, stop the app, recreate only `mujian`, restore, then
start the same compatibility release:

```sh
backup_file=/absolute/path/to/mujian-TIMESTAMP.dump
test "${RESTORE_CONFIRM_DATABASE:-}" = mujian
test "${RESTORE_CONFIRM_DATA_LOSS:-}" = YES
expected_backup_sha=$(awk 'NR == 1 { print $1 }' "$backup_file.sha256")
actual_backup_sha=$(sha256sum "$backup_file" | awk '{ print $1 }')
test -n "$expected_backup_sha"
test "$actual_backup_sha" = "$expected_backup_sha"

docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml stop app
docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  exec -T postgres sh -c 'test "$POSTGRES_DB" = mujian'
docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  exec -T postgres sh -c \
  'dropdb --if-exists --force --username="$POSTGRES_USER" "$POSTGRES_DB" && createdb --username="$POSTGRES_USER" "$POSTGRES_DB"'
docker compose --env-file deploy/runtime/mujian.env -f docker-compose.mujian.yml \
  exec -T postgres sh -c \
  'exec pg_restore --exit-on-error --no-owner --no-acl --username="$POSTGRES_USER" --dbname="$POSTGRES_DB"' \
  < "$backup_file"
deploy/scripts/deploy-release.sh up
```

Do not delete OBS objects during database rollback. Reconcile the object ledger
and inventory first; object deletion is a separate, reviewed operation.
