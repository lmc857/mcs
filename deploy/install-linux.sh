#!/usr/bin/env bash
# MCS Linux installer (systemd). Run as root from the repo root:
#   sudo ./deploy/install-linux.sh dist/mcs-linux-amd64
set -euo pipefail

BIN="${1:-dist/mcs-linux-amd64}"
CONFIG="${2:-config.example.yaml}"
SERVICE_SRC="$(cd "$(dirname "$0")" && pwd)/mcs.service"

if [[ "${EUID}" -ne 0 ]]; then
  echo "please run as root (sudo $0 $*)" >&2
  exit 1
fi
if [[ ! -f "${BIN}" ]]; then
  echo "binary not found: ${BIN}" >&2
  exit 1
fi

install -d -m 0755 /opt/mcs /etc/mcs /var/lib/mcs
install -m 0755 "${BIN}" /opt/mcs/mcs
if ! id -u mcs >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/mcs --shell /usr/sbin/nologin mcs
fi
if [[ ! -f /etc/mcs/config.yaml ]]; then
  install -m 0640 -g mcs "${CONFIG}" /etc/mcs/config.yaml
  echo "installed default config: /etc/mcs/config.yaml (edit advertise/domain/devices)"
fi
chown root:mcs /etc/mcs/config.yaml
chmod 0640 /etc/mcs/config.yaml
chown -R mcs:mcs /var/lib/mcs
install -m 0644 "${SERVICE_SRC}" /etc/systemd/system/mcs.service
systemctl daemon-reload
systemctl enable --now mcs
echo
echo "MCS installed. Status:"
systemctl --no-pager --full status mcs || true
echo
echo "Next: edit /etc/mcs/config.yaml, then systemctl restart mcs"
echo "Web console: http://<host>:8080   SIP: udp/5060"
