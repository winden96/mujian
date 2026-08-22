#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "run this script as root" >&2
  exit 1
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_config=$(cd -- "$script_dir/.." && pwd)/nginx/mujian.conf
renewal_hook=$(cd -- "$script_dir/.." && pwd)/nginx/reload-after-renewal.sh
target_config=/etc/nginx/sites-available/mujian.conf

if [[ ! -f /etc/letsencrypt/live/www.mujianai.com/fullchain.pem ]]; then
  echo "issue the www.mujianai.com certificate before enabling the HTTPS template" >&2
  exit 1
fi

install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
install -m 0755 "$renewal_hook" /etc/letsencrypt/renewal-hooks/deploy/mujian-nginx-reload
systemctl enable --now certbot.timer
systemctl is-enabled --quiet certbot.timer
systemctl is-active --quiet certbot.timer
certbot renew --dry-run --cert-name www.mujianai.com

install -m 0644 "$source_config" "$target_config"
ln -sfn "$target_config" /etc/nginx/sites-enabled/mujian.conf
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable --now nginx
systemctl reload nginx
