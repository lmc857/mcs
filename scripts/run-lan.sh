#!/usr/bin/env bash
# LAN test instance: binds 0.0.0.0 and advertises the host LAN IP.
# Usage: LAN_IP=192.168.1.10 make run-lan
set -euo pipefail
cd "$(dirname "$0")/.."

LAN_IP="${LAN_IP:-}"
LAN_IF="${LAN_IF:-}"
if [[ -z "$LAN_IP" && -z "$LAN_IF" ]]; then
  LAN_IF="$(route -n get default 2>/dev/null | awk '/interface:/{print $2; exit}')"
fi
if [[ -z "$LAN_IP" && -n "$LAN_IF" ]]; then
  LAN_IP="$(ipconfig getifaddr "$LAN_IF" 2>/dev/null || true)"
fi
if [[ -z "$LAN_IP" ]]; then
  LAN_IP="$(ipconfig getifaddr en0 2>/dev/null || true)"
fi
if [[ -z "$LAN_IP" ]]; then
  LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
fi
if [[ -z "$LAN_IP" ]]; then
  echo "cannot detect LAN IP; set LAN_IP=192.168.x.x" >&2
  exit 1
fi

SIP_PORT="${SIP_PORT:-5060}"
HTTP_PORT="${HTTP_PORT:-18080}"
HTTPS_PORT="${HTTPS_PORT:-18443}"
DATA_DIR="${DATA_DIR:-./data-lan}"
CERT_DIR="$DATA_DIR/dev-tls"
CONFIG=".run/config.lan.yaml"

if [[ ! -x bin/mcs ]]; then
  echo "bin/mcs not found, running make build..."
  make build
fi
mkdir -p "$CERT_DIR" .run "$DATA_DIR"
if [[ ! -f "$CERT_DIR/cert.pem" || ! -f "$CERT_DIR/key.pem" ]]; then
  echo "generating self-signed cert for $LAN_IP in $CERT_DIR"
  openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
    -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
    -subj "/CN=$LAN_IP" \
    -addext "subjectAltName=IP:$LAN_IP,IP:127.0.0.1,DNS:localhost" >/dev/null 2>&1 || \
  openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
    -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
    -subj "/CN=$LAN_IP" >/dev/null 2>&1
fi

sed -e "s|sip_udp: \"0.0.0.0:5060\"|sip_udp: \"0.0.0.0:${SIP_PORT}\"|" \
    -e "s|http: \"127.0.0.1:8080\"|http: \"0.0.0.0:${HTTP_PORT}\"|" \
    -e "s|advertise: \"127.0.0.1:5060\"|advertise: \"${LAN_IP}:${SIP_PORT}\"|" \
    -e "s|data_dir: \"./data\"|data_dir: \"${DATA_DIR}\"|" \
    config.example.yaml > "$CONFIG"

python3 - "$CONFIG" "$CERT_DIR" "$HTTPS_PORT" <<'PY'
import sys
p, cert_dir, https_port = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p, encoding="utf-8").read()
needle = '  http: "0.0.0.0:'
idx = s.find(needle)
if idx < 0:
    raise SystemExit("http line not found")
end = s.find("\n", idx)
block = (
    f'\n  https: "0.0.0.0:{https_port}"'
    f'\n  https_cert: "{cert_dir}/cert.pem"'
    f'\n  https_key: "{cert_dir}/key.pem"'
)
s = s[:end] + block + s[end:]
open(p, "w", encoding="utf-8").write(s)
PY

cat <<MSG
MCS LAN test server:
  LAN IP      : ${LAN_IP}${LAN_IF:+ (${LAN_IF})}
  SIP UDP     : ${LAN_IP}:${SIP_PORT}
  HTTP console: http://${LAN_IP}:${HTTP_PORT}/
  HTTPS       : https://${LAN_IP}:${HTTPS_PORT}/
  data dir    : ${DATA_DIR}
  config      : ${CONFIG}

Configure devices with SIP server ${LAN_IP}:${SIP_PORT} (UDP).
Allow inbound TCP ${HTTP_PORT}/${HTTPS_PORT} and UDP ${SIP_PORT}; RTP uses dynamic UDP ports.
Press Ctrl-C to stop.
MSG
exec ./bin/mcs -config "$CONFIG"
