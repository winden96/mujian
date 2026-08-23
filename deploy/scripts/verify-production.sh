#!/usr/bin/env bash
set -Eeuo pipefail

app_url=https://www.mujianai.com
root_url=https://mujianai.com
http_url=http://www.mujianai.com
tls_host=www.mujianai.com
public_host=1.92.114.137
test_username=${MUJIAN_TEST_USERNAME:-}
test_password=${MUJIAN_TEST_PASSWORD:-}
image_url=${MUJIAN_TEST_IMAGE_URL:-}
private_probe_url=${MUJIAN_PRIVATE_PROBE_URL:-https://static.mujianai.com/mujian/prod/private/verification-probe}
curl_command=(curl --disable --connect-timeout 10 --max-time 30)

if [[ -z $test_username || -z $test_password || -z $image_url ]]; then
  echo "MUJIAN_TEST_USERNAME, MUJIAN_TEST_PASSWORD, and MUJIAN_TEST_IMAGE_URL are required" >&2
  exit 1
fi

headers=$(mktemp)
certificate=$(mktemp)
response_body=$(mktemp)
trap 'rm -f "$headers" "$certificate" "$response_body"' EXIT

python3 - "$image_url" "$private_probe_url" <<'PY'
import sys
import urllib.parse

public_url = urllib.parse.urlsplit(sys.argv[1])
private_url = urllib.parse.urlsplit(sys.argv[2])
public_prefix = "/mujian/prod/public/"

def is_unsigned_static_url(url):
    return (
        url.scheme == "https"
        and url.hostname == "static.mujianai.com"
        and url.port is None
        and url.username is None
        and url.password is None
        and not url.query
        and not url.fragment
    )

if not is_unsigned_static_url(public_url) or not public_url.path.startswith(public_prefix):
    raise SystemExit("MUJIAN_TEST_IMAGE_URL must be an unsigned static.mujianai.com public-prefix URL")
if not is_unsigned_static_url(private_url) or private_url.path.startswith(public_prefix):
    raise SystemExit("MUJIAN_PRIVATE_PROBE_URL must be an unsigned non-public static.mujianai.com URL")
PY

"${curl_command[@]}" --fail --silent --show-error --dump-header "$headers" \
  --output "$response_body" "$app_url/api/status"
grep -q '"success":true' "$response_body"
grep -Eiq '^strict-transport-security:.*max-age=' "$headers"
grep -Eiq '^x-content-type-options:[[:space:]]*nosniff' "$headers"

"${curl_command[@]}" --silent --show-error --dump-header "$headers" \
  --output /dev/null "$root_url/api/status"
grep -Eq '^HTTP/[^ ]+ (301|308)' "$headers"
grep -Eiq '^location:[[:space:]]*https://www\.mujianai\.com/api/status' "$headers"
grep -Eiq '^strict-transport-security:.*max-age=' "$headers"

"${curl_command[@]}" --silent --show-error --dump-header "$headers" \
  --output /dev/null "$http_url/api/status"
grep -Eq '^HTTP/[^ ]+ (301|308)' "$headers"
grep -Eiq '^location:[[:space:]]*https://www\.mujianai\.com/api/status' "$headers"

python3 - "$tls_host" <<'PY' >"$certificate"
import socket
import ssl
import sys

host = sys.argv[1]
context = ssl.create_default_context()
with socket.create_connection((host, 443), timeout=15) as connection:
    with context.wrap_socket(connection, server_hostname=host) as tls:
        print(ssl.DER_cert_to_PEM_cert(tls.getpeercert(binary_form=True)), end="")
PY
openssl x509 -in "$certificate" -noout -checkhost "$tls_host"
openssl x509 -in "$certificate" -noout -checkhost mujianai.com
openssl x509 -in "$certificate" -noout -checkend 2592000

python3 - "$public_host" 10088 5432 6379 <<'PY'
import socket
import sys

host = sys.argv[1]
reachable = []
for raw_port in sys.argv[2:]:
    port = int(raw_port)
    try:
        connection = socket.create_connection((host, port), timeout=3)
    except OSError:
        continue
    else:
        connection.close()
        reachable.append(port)
if reachable:
    raise SystemExit(f"unexpected public TCP ports: {reachable}")
PY

printf '%s' "$test_password" \
  | MUJIAN_TEST_USERNAME=$test_username \
    python3 -c 'import json, os, sys; print(json.dumps({"username": os.environ["MUJIAN_TEST_USERNAME"], "password": sys.stdin.read()}))' \
  | "${curl_command[@]}" --fail --silent --show-error --dump-header "$headers" --output "$response_body" \
    -H 'Content-Type: application/json' --data-binary @- "$app_url/api/user/login"
python3 - "$headers" "$response_body" <<'PY'
import json
import pathlib
import sys

response = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
if response.get("success") is not True:
    raise SystemExit("production login response did not report success")
if isinstance(response.get("data"), dict) and response["data"].get("require_2fa") is True:
    raise SystemExit("test account requires 2FA and did not complete an authenticated login")
cookies = [
    line.casefold()
    for line in pathlib.Path(sys.argv[1]).read_text(encoding="iso-8859-1").splitlines()
    if line.casefold().startswith("set-cookie:")
]
required = ("httponly", "secure", "samesite=strict")
if not any(all(attribute in cookie for attribute in required) for cookie in cookies):
    raise SystemExit("no single Set-Cookie header contains HttpOnly, Secure, and SameSite=Strict")
PY

"${curl_command[@]}" --fail --silent --show-error --head "$image_url" >"$headers"
grep -Eiq '^content-type:[[:space:]]*image/' "$headers"
grep -Eiq '^content-disposition:[[:space:]]*inline' "$headers"
grep -Eiq '^cache-control:.*max-age=31536000' "$headers"
grep -Eiq '^cache-control:.*immutable' "$headers"
"${curl_command[@]}" --fail --silent --show-error --head \
  -H 'Origin: https://www.mujianai.com' "$image_url" >"$headers"
grep -Eiq '^access-control-allow-origin:[[:space:]]*https://www\.mujianai\.com' "$headers"

private_status=$("${curl_command[@]}" --silent --show-error --output /dev/null \
  --write-out '%{http_code}' "$private_probe_url")
if [[ $private_status != 403 ]]; then
  echo "non-public OBS prefix returned HTTP $private_status, expected 403" >&2
  exit 1
fi

echo "production acceptance checks passed"
