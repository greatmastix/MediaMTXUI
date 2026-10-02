#!/usr/bin/env bash
# Removes mtx-portgate: closes everything it opened first, then removes the units and the binary. Keeps
# /etc/mtx-portgate (the policy) and the sidecar's request directory.
set -euo pipefail
[[ $(id -u) -eq 0 ]] || { echo "uninstall.sh: run as root" >&2; exit 1; }
if [[ -x /usr/local/sbin/mtx-portgate ]]; then
  /usr/local/sbin/mtx-portgate close-all >/dev/null
fi
systemctl disable --now mtx-portgate.path mtx-portgate.timer 2>/dev/null || true
rm -f /etc/systemd/system/mtx-portgate.{service,path,timer} /usr/local/sbin/mtx-portgate
systemctl daemon-reload
rm -rf /var/lib/mtx-portgate
echo "mtx-portgate removed; ufw has no mtx-portgate: rules left:"
ufw show added | grep -c "mtx-portgate:" || true
