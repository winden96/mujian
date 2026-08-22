#!/usr/bin/env bash
set -Eeuo pipefail

test_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$test_dir/../../.." && pwd)
release_script="$repo_root/deploy/scripts/deploy-release.sh"
verify_script="$repo_root/deploy/scripts/verify-production.sh"
nginx_script="$repo_root/deploy/scripts/configure-nginx.sh"
compose_file="$repo_root/docker-compose.mujian.yml"
dockerfile="$repo_root/Dockerfile.mujian"
runbook="$repo_root/deploy/README.md"

fail() {
  echo "deploy hardening static test failed: $*" >&2
  exit 1
}

require_literal() {
  local file=$1
  local value=$2
  grep -Fq -- "$value" "$file" || fail "$file is missing: $value"
}

bash -n \
  "$release_script" \
  "$verify_script" \
  "$nginx_script" \
  "$repo_root/deploy/scripts/configure-nginx-http.sh" \
  "$repo_root/deploy/scripts/install-host-dependencies.sh"

require_literal "$dockerfile" 'org.opencontainers.image.source'
require_literal "$dockerfile" 'org.opencontainers.image.revision'
require_literal "$dockerfile" 'org.opencontainers.image.version'
require_literal "$compose_file" '127.0.0.1:10088:3000'
require_literal "$compose_file" 'command: ["--log-dir", ""]'
require_literal "$compose_file" "SOURCE_REVISION: \${MUJIAN_IMAGE_TAG:"
require_literal "$compose_file" "/\${MUJIAN_DATABASE_NAME:"

require_literal "$release_script" 'build|up|rollback-db'
require_literal "$release_script" "\${image_tag}-candidate"
require_literal "$release_script" 'refusing to replace immutable'
require_literal "$release_script" "require_recorded_image \"\$image_ref\""
require_literal "$release_script" 'GIT_CONFIG_GLOBAL=/dev/null'
require_literal "$release_script" 'refs/heads/main'
require_literal "$release_script" '--no-build --pull never'

require_literal "$verify_script" 'MUJIAN_TEST_USERNAME, MUJIAN_TEST_PASSWORD, and MUJIAN_TEST_IMAGE_URL are required'
require_literal "$verify_script" 'no single Set-Cookie header contains HttpOnly, Secure, and SameSite=Strict'
require_literal "$verify_script" 'non-public OBS prefix returned HTTP'
if grep -Fq 'skipped' "$verify_script"; then
  fail 'production verification must not silently skip an acceptance check'
fi

require_literal "$nginx_script" '/etc/letsencrypt/renewal-hooks/deploy/mujian-nginx-reload'
require_literal "$nginx_script" 'systemctl enable --now certbot.timer'
require_literal "$nginx_script" 'certbot renew --dry-run --cert-name www.mujianai.com'

require_literal "$repo_root/deploy/scripts/install-host-dependencies.sh" "dpkg-query -S \"\$pgloader_path\""
require_literal "$repo_root/deploy/scripts/install-host-dependencies.sh" 'expected Ubuntu pgloader package 3.6.10-1build2'
require_literal "$repo_root/deploy/scripts/install-host-dependencies.sh" "\"\$pgloader_path\" --version"

rehearsal_line=$(grep -n '^## 3\. Rehearse' "$runbook" | cut -d: -f1)
cutover_line=$(grep -n '^## 4\. Final cutover' "$runbook" | cut -d: -f1)
dns_line=$(grep -n '^## 5\. Enable DNS' "$runbook" | cut -d: -f1)
[[ -n $rehearsal_line && -n $cutover_line && -n $dns_line ]] || fail 'rollout sections are missing'
(( rehearsal_line < cutover_line && cutover_line < dns_line )) || \
  fail 'rehearsal and final local verification must precede DNS/HTTPS traffic'

echo "deploy hardening static checks passed"
