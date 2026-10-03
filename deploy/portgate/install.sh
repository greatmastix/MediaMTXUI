#!/usr/bin/env bash
# Installs mtx-portgate, the exposure-control helper (docs/exposure-control.md). Run as root from this directory:
#   sudo ./install.sh [image]        the image the stack runs; default ghcr.io/greatmastix/mediamtxui:latest
# REQUESTS_DIR (default /var/lib/mtx-portgate-requests) is where the sidecar writes its requests: deploy/compose.
# exposure.yaml mounts it as PORTGATE_DIR. Idempotent. An existing /etc/mtx-portgate/policy.json is kept (the
# operator's ceiling); FORCE_POLICY=1 replaces it with this directory's. Undo with ./uninstall.sh.
set -euo pipefail
cd "$(dirname "$0")"
image=${1:-ghcr.io/greatmastix/mediamtxui:latest}
requests=${REQUESTS_DIR:-/var/lib/mtx-portgate-requests}
[[ $(id -u) -eq 0 ]] || { echo "install.sh: run as root" >&2; exit 1; }
command -v ufw >/dev/null || { echo "install.sh: the helper drives ufw, which is not installed" >&2; exit 1; }

# The binary comes from the image the stack runs, so helper and sidecar speak the same file formats.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"; [[ -n ${cid:-} ]] && docker rm "$cid" >/dev/null' EXIT
cid=$(docker create "$image")
docker cp "$cid:/usr/libexec/mtxui/mtx-portgate" "$tmp/mtx-portgate"
install -m 755 -o root -g root "$tmp/mtx-portgate" /usr/local/sbin/mtx-portgate

install -d -m 755 -o root -g root /etc/mtx-portgate /var/lib/mtx-portgate
if [[ ! -e /etc/mtx-portgate/policy.json || ${FORCE_POLICY:-0} == 1 ]]; then
  sed "s|/var/lib/mtx-portgate-requests|$requests|" policy.json >"$tmp/policy.json"
  install -m 644 -o root -g root "$tmp/policy.json" /etc/mtx-portgate/policy.json
fi
/usr/local/sbin/mtx-portgate check-policy

# The sidecar (uid 10002) writes its requests here; the helper reads them without following symlinks.
install -d -m 700 -o 10002 -g 10002 "$requests"

for u in mtx-portgate.service mtx-portgate.timer; do
  install -m 644 -o root -g root "$u" "/etc/systemd/system/$u"
done
sed "s|/var/lib/mtx-portgate-requests|$requests|" mtx-portgate.path >"$tmp/mtx-portgate.path"
install -m 644 -o root -g root "$tmp/mtx-portgate.path" /etc/systemd/system/mtx-portgate.path
systemctl daemon-reload
systemctl enable --now mtx-portgate.path mtx-portgate.timer
systemctl start mtx-portgate.service # first status: every port closed until the sidecar asks
/usr/local/sbin/mtx-portgate status
