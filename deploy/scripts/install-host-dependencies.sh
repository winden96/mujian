#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --allow-downgrades --allow-change-held-packages \
  ca-certificates \
  certbot \
  coreutils \
  curl \
  git \
  nginx \
  openssl \
  pgloader=3.6.10-1build2 \
  postgresql-client \
  python3 \
  python3-certbot-nginx \
  sqlite3

if ! command -v docker >/dev/null 2>&1; then
  apt-get install -y docker.io docker-compose-v2
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required; install the plugin matching the existing Docker distribution" >&2
  exit 1
fi

apt-mark hold pgloader
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

install -d -m 0755 /opt/mujian /opt/mujian/releases /var/www/certbot
systemctl enable --now docker
systemctl enable nginx

docker version
docker compose version
nginx -v
"$pgloader_path" --version
