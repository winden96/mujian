#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_config=$(cd -- "$script_dir/.." && pwd)/nginx/mujian-http.conf
target_config=/etc/nginx/sites-available/mujian.conf

install -d -m 0755 /var/www/certbot
install -m 0644 "$source_config" "$target_config"
ln -sfn "$target_config" /etc/nginx/sites-enabled/mujian.conf
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable --now nginx
systemctl reload nginx
