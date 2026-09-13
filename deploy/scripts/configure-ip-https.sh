#!/usr/bin/env bash
set -Eeuo pipefail

if [[ ${EUID} -ne 0 || $# -ne 2 ]]; then
  echo "usage (root): $0 <current-release-directory> <existing-backup-directory>" >&2
  exit 1
fi

release=$1
backup=$2
deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
runtime=/opt/mujian/runtime/mujian.env
override=/opt/mujian/runtime/docker-compose.public-port.yml
target=/etc/nginx/sites-available/mujian.conf
cert_dir=/etc/letsencrypt-ip/live/1.92.114.137

test -f "$release/docker-compose.mujian.yml"
test -f "$backup/nginx/sites-available/mujian.conf"
test -f "$backup/docker-compose.public-port.yml"
openssl x509 -in "$cert_dir/cert.pem" -noout -checkip 1.92.114.137
openssl x509 -in "$cert_dir/cert.pem" -noout -checkend 86400
openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt \
  -untrusted "$cert_dir/chain.pem" "$cert_dir/cert.pem"

compose() {
  docker compose --project-name mujian-newapi --env-file "$runtime" \
    -f "$release/docker-compose.mujian.yml" -f "$override" "$@"
}

rollback() {
  trap - ERR
  cp -p "$backup/nginx/sites-available/mujian.conf" "$target"
  nginx -t && systemctl reload nginx
  cp -p "$backup/docker-compose.public-port.yml" "$override"
  compose up -d --no-deps app
}
trap rollback ERR

install -m 0644 "$deploy_dir/nginx/mujian-ip.conf" "$target"
nginx -t
install -m 0600 "$deploy_dir/docker-compose.ip-https.yml" "$override"
compose config --quiet
# Free the old public port before nginx takes it over. The application keeps
# its image, environment, session secret and internal port unchanged.
compose up -d --no-deps app
python3 - <<'PY'
import json
import time
import urllib.request

for attempt in range(25):
    try:
        with urllib.request.urlopen('http://127.0.0.1:10090/api/status', timeout=2) as response:
            if json.load(response)['success']:
                break
    except (OSError, ValueError):
        pass
    time.sleep(1)
else:
    raise SystemExit('Application failed health check on loopback port 10090')
PY
systemctl reload nginx
curl --fail --silent --show-error --max-time 10 \
  --resolve 1.92.114.137:443:127.0.0.1 https://1.92.114.137/api/status >/dev/null

install -m 0755 "$deploy_dir/nginx/reload-after-renewal.sh" /opt/mujian/certbot-ip/reload-nginx.sh
/opt/mujian/certbot-ip/venv/bin/certbot reconfigure \
  --cert-name 1.92.114.137 --non-interactive --run-deploy-hooks \
  --config-dir /etc/letsencrypt-ip --work-dir /var/lib/letsencrypt-ip \
  --logs-dir /var/log/letsencrypt-ip \
  --deploy-hook /opt/mujian/certbot-ip/reload-nginx.sh
install -m 0644 "$deploy_dir/nginx/mujian-ip-cert-renew.service" /etc/systemd/system/
install -m 0644 "$deploy_dir/nginx/mujian-ip-cert-renew.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now mujian-ip-cert-renew.timer
systemctl is-active --quiet mujian-ip-cert-renew.timer
trap - ERR
echo 'IP HTTPS configured; public browser acceptance required'
