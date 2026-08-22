#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  echo "usage: $0 build|up|rollback-db" >&2
}

if (( $# != 1 )); then
  usage
  exit 1
fi

mode=$1
if [[ $mode != build && $mode != up && $mode != rollback-db ]]; then
  usage
  exit 1
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
compose_file="$repo_root/docker-compose.mujian.yml"
env_file=${MUJIAN_ENV_FILE:-"$repo_root/deploy/runtime/mujian.env"}
public_repository=https://github.com/winden96/mujian.git
artifact_temporary=

cleanup() {
  if [[ -n $artifact_temporary && -f $artifact_temporary ]]; then
    rm -f -- "$artifact_temporary"
  fi
}
trap cleanup EXIT

if [[ ! -f $env_file ]]; then
  echo "runtime environment file not found: $env_file" >&2
  exit 1
fi

env_mode=$(stat -c '%a' "$env_file")
if [[ $env_mode != 600 && $env_mode != 400 ]]; then
  echo "runtime environment file must have mode 0600 or 0400, got $env_mode" >&2
  exit 1
fi

read_env() {
  local key=$1
  sed -n "s/^${key}=//p" "$env_file" | tail -n 1 | tr -d '\r'
}

require_env_value() {
  local key=$1
  local expected=$2
  local actual
  actual=$(read_env "$key")
  if [[ $actual != "$expected" ]]; then
    echo "$key must equal $expected" >&2
    exit 1
  fi
}

require_env_value MUJIAN_PRODUCTION true
require_env_value MUJIAN_DEMO_RECHARGE_ENABLED false
require_env_value FRONTEND_BASE_URL https://www.mujianai.com
require_env_value OBS_AUTH_MODE ecs
require_env_value OBS_REGION cn-north-4
require_env_value OBS_ENDPOINT https://obs.cn-north-4.myhuaweicloud.com
require_env_value OBS_BUCKET mujianai
require_env_value OBS_PREFIX mujian/prod/public
require_env_value OBS_PUBLIC_BASE_URL https://static.mujianai.com

expected_storage=obs
if [[ $mode == rollback-db ]]; then
  expected_storage=db
fi
require_env_value MUJIAN_IMAGE_STORAGE "$expected_storage"

image_tag=$(read_env MUJIAN_IMAGE_TAG)
app_version=$(read_env MUJIAN_APP_VERSION)
source_url=$(read_env AGPL_SOURCE_URL)
postgres_password=$(read_env POSTGRES_PASSWORD)
session_secret=$(read_env SESSION_SECRET)
crypto_secret=$(read_env CRYPTO_SECRET)
postgres_database=$(read_env POSTGRES_DB)
application_database=$(read_env MUJIAN_DATABASE_NAME)
head_sha=$(git -C "$repo_root" rev-parse HEAD)
image_ref="mujian-newapi:$image_tag"
artifact_dir="$repo_root/deploy/reports"
artifact_file="$artifact_dir/release-$image_tag.txt"

if [[ $postgres_database != mujian || $application_database != "$postgres_database" ]]; then
  echo "production POSTGRES_DB and MUJIAN_DATABASE_NAME must both equal mujian" >&2
  exit 1
fi

if [[ ! $image_tag =~ ^[0-9a-f]{40}$ ]]; then
  echo "MUJIAN_IMAGE_TAG must be a complete lowercase commit SHA" >&2
  exit 1
fi
if [[ $image_tag != "$head_sha" ]]; then
  echo "runtime image tag $image_tag does not match checked-out source $head_sha" >&2
  exit 1
fi
if [[ ! $app_version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "MUJIAN_APP_VERSION must be an immutable semantic release tag" >&2
  exit 1
fi
if ! git -C "$repo_root" tag --points-at HEAD | grep -Fxq "$app_version"; then
  echo "checked-out source is not tagged $app_version" >&2
  exit 1
fi
expected_source_url="https://github.com/winden96/mujian/tree/$image_tag"
if [[ $source_url != "$expected_source_url" ]]; then
  echo "AGPL_SOURCE_URL must equal $expected_source_url" >&2
  exit 1
fi
if [[ $postgres_password == replace-with-* || ! $postgres_password =~ ^[A-Za-z0-9_-]{32,}$ ]]; then
  echo "POSTGRES_PASSWORD must be at least 32 URL-safe characters because it is embedded in SQL_DSN" >&2
  exit 1
fi
if [[ $session_secret == replace-with-* || $crypto_secret == replace-with-* ]] ||
  (( ${#session_secret} < 32 || ${#crypto_secret} < 32 )); then
  echo "SESSION_SECRET and CRYPTO_SECRET must each contain at least 32 characters" >&2
  exit 1
fi
if [[ $session_secret == "$crypto_secret" ]]; then
  echo "SESSION_SECRET and CRYPTO_SECRET must be different values" >&2
  exit 1
fi
if grep -Eq '^(OBS_ACCESS_KEY|OBS_ACCESS_KEY_ID|OBS_SECRET_KEY|OBS_SECRET_ACCESS_KEY|OBS_SECURITY_TOKEN|OBS_AK|OBS_SK)=' "$env_file"; then
  echo "explicit OBS credentials are forbidden; use OBS_AUTH_MODE=ecs" >&2
  exit 1
fi
if [[ -n $(git -C "$repo_root" status --short) ]]; then
  echo "refusing to deploy a dirty source tree" >&2
  exit 1
fi

compose=(docker compose --env-file "$env_file" -f "$compose_file")

image_id() {
  docker image inspect --format '{{.Id}}' "$1" 2>/dev/null
}

require_image_labels() {
  local ref=$1
  local actual_source actual_revision actual_version
  actual_source=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.source"}}' "$ref")
  actual_revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ref")
  actual_version=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$ref")
  if [[ $actual_source != "$source_url" || $actual_revision != "$image_tag" || $actual_version != "$app_version" ]]; then
    echo "image OCI labels do not match source URL, commit SHA, and release tag" >&2
    exit 1
  fi
}

record_image_artifact() {
  local ref=$1
  local content_digest repo_digests created_at recorded_digest
  content_digest=$(image_id "$ref")
  repo_digests=$(docker image inspect --format '{{json .RepoDigests}}' "$ref")
  created_at=$(docker image inspect --format '{{.Created}}' "$ref")

  install -d -m 0700 "$artifact_dir"
  if [[ -f $artifact_file ]]; then
    recorded_digest=$(sed -n 's/^image_content_digest=//p' "$artifact_file")
    if [[ $recorded_digest != "$content_digest" ]]; then
      echo "artifact $artifact_file records $recorded_digest, not $content_digest" >&2
      exit 1
    fi
    return
  fi

  artifact_temporary=$(mktemp "$artifact_dir/.release-$image_tag.XXXXXX")
  chmod 0600 "$artifact_temporary"
  {
    printf 'source_sha=%s\n' "$image_tag"
    printf 'release_tag=%s\n' "$app_version"
    printf 'source_url=%s\n' "$source_url"
    printf 'image_ref=%s\n' "$image_ref"
    printf 'image_content_digest=%s\n' "$content_digest"
    printf 'image_repo_digests=%s\n' "$repo_digests"
    printf 'image_created_at=%s\n' "$created_at"
  } >"$artifact_temporary"
  mv "$artifact_temporary" "$artifact_file"
  artifact_temporary=
}

require_recorded_image() {
  local ref=$1
  local content_digest recorded_digest
  if [[ ! -f $artifact_file ]]; then
    echo "release artifact is missing: $artifact_file; run '$0 build' first" >&2
    exit 1
  fi
  content_digest=$(image_id "$ref")
  recorded_digest=$(sed -n 's/^image_content_digest=//p' "$artifact_file")
  if [[ $recorded_digest != "$content_digest" ]]; then
    echo "artifact $artifact_file records $recorded_digest, not $content_digest" >&2
    exit 1
  fi
}

verify_public_source() {
  local remote_refs main_sha direct_tag_sha peeled_tag_sha tag_sha
  if ! remote_refs=$(
    cd /
    env -u GH_TOKEN -u GITHUB_TOKEN GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
      GIT_CONFIG_COUNT=0 GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false \
      timeout --signal=TERM 45s \
      git -c credential.helper= -c http.extraHeader= -c http.lowSpeedLimit=1 -c http.lowSpeedTime=30 \
      ls-remote "$public_repository" refs/heads/main \
      "refs/tags/$app_version" "refs/tags/$app_version^{}"
  ); then
    echo "public source repository is not anonymously readable" >&2
    exit 1
  fi
  main_sha=$(awk '$2 == "refs/heads/main" { print $1 }' <<<"$remote_refs")
  direct_tag_sha=$(awk -v ref="refs/tags/$app_version" '$2 == ref { print $1 }' <<<"$remote_refs")
  peeled_tag_sha=$(awk -v ref="refs/tags/$app_version^{}" '$2 == ref { print $1 }' <<<"$remote_refs")
  tag_sha=${peeled_tag_sha:-$direct_tag_sha}
  if [[ $main_sha != "$image_tag" ]]; then
    echo "public main resolves to ${main_sha:-missing}, expected $image_tag" >&2
    exit 1
  fi
  if [[ $tag_sha != "$image_tag" ]]; then
    echo "public tag $app_version resolves to ${tag_sha:-missing}, expected $image_tag" >&2
    exit 1
  fi
  curl --disable --fail --silent --show-error --location --max-redirs 3 \
    --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 30 \
    --header 'Authorization:' --cookie '' --output /dev/null "$expected_source_url"
}

"${compose[@]}" config --quiet

if [[ $mode == build ]]; then
  candidate_ref="mujian-newapi:${image_tag}-candidate"
  existing_id=$(image_id "$image_ref" || true)
  docker build --pull \
    --file "$repo_root/Dockerfile.mujian" \
    --build-arg "AGPL_SOURCE_URL=$source_url" \
    --build-arg "APP_VERSION=$app_version" \
    --build-arg "SOURCE_REVISION=$image_tag" \
    --tag "$candidate_ref" \
    "$repo_root"
  candidate_id=$(image_id "$candidate_ref")
  require_image_labels "$candidate_ref"
  if [[ -n $existing_id && $existing_id != "$candidate_id" ]]; then
    echo "refusing to replace immutable $image_ref ($existing_id) with $candidate_id" >&2
    echo "candidate retained as $candidate_ref for investigation" >&2
    exit 1
  fi
  if [[ -z $existing_id ]]; then
    docker image tag "$candidate_ref" "$image_ref"
  fi
  require_image_labels "$image_ref"
  record_image_artifact "$image_ref"
  cat "$artifact_file"
  exit 0
fi

verify_public_source
if ! image_id "$image_ref" >/dev/null; then
  echo "immutable image is missing; run '$0 build' first" >&2
  exit 1
fi
require_image_labels "$image_ref"
require_recorded_image "$image_ref"
"${compose[@]}" up -d --no-build --pull never --remove-orphans --wait --wait-timeout 120

if ! curl --disable --fail --silent --show-error --connect-timeout 2 --max-time 10 \
  http://127.0.0.1:10088/api/status | grep -q '"success":true'; then
  "${compose[@]}" ps >&2
  "${compose[@]}" logs --tail=200 app >&2
  echo "application health endpoint did not return success" >&2
  exit 1
fi

"${compose[@]}" ps
"${compose[@]}" images
cat "$artifact_file"
