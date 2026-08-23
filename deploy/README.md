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
