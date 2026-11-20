#!/usr/bin/env bash
# Local HTTPS console for development.
# Generates a self-signed cert and a throwaway config, then runs MCS in foreground.
set -euo pipefail
cd "$(dirname "$0")/.."

SIP_PORT="${SIP_PORT:-15060}"
HTTP_PORT="${HTTP_PORT:-38084}"
HTTPS_PORT="${HTTPS_PORT:-38443}"
CERT_DIR="data/dev-tls"
CONFIG=".run/config.https.yaml"

if [[ ! -x bin/mcs ]]; then
  echo "bin/mcs not found, running make build..."
  make build
fi
mkdir -p "$CERT_DIR" .run
if [[ ! -f "$CERT_DIR/cert.pem" || ! -f "$CERT_DIR/key.pem" ]]; then
  echo "generating self-signed cert in $CERT_DIR"
  if ! openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
      -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
      -subj "/CN=localhost" \
      -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
      -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
      -subj "/CN=localhost" >/dev/null 2>&1
  fi
fi

sed -e "s|0.0.0.0:5060|127.0.0.1:${SIP_PORT}|" \
    -e "s|127.0.0.1:8080|127.0.0.1:${HTTP_PORT}|" \
    -e "s|127.0.0.1:5060|127.0.0.1:${SIP_PORT}|" \
    config.example.yaml > "$CONFIG"

python3 - "$CONFIG" "$CERT_DIR" "$HTTPS_PORT" <<'PY'
import sys
p, cert_dir, https_port = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
needle = f'  http: "127.0.0.1:'
idx = s.find(needle)
if idx < 0:
    raise SystemExit("http line not found")
end = s.find("\n", idx)
block = (
    f'\n  https: "127.0.0.1:{https_port}"'
    f'\n  https_cert: "{cert_dir}/cert.pem"'
    f'\n  https_key: "{cert_dir}/key.pem"'
)
s = s[:end] + block + s[end:]
open(p, "w", encoding="utf-8").write(s)
PY

cat <<MSG
MCS HTTPS console:
  dashboard : https://127.0.0.1:${HTTPS_PORT}/
  session   : https://127.0.0.1:${HTTPS_PORT}/api/v1/session
  HTTP      : http://127.0.0.1:${HTTP_PORT}/
  SIP UDP   : 127.0.0.1:${SIP_PORT}
  config    : ${CONFIG}
The cert is self-signed: accept the browser warning (Advanced -> Proceed).
Ctrl-C to stop.
MSG
exec ./bin/mcs -config "$CONFIG"
