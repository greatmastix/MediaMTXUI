#!/usr/bin/env bash
# Removes mtx-portgate: closes everything it opened first, then removes the units and the binary. Keeps
# /etc/mtx-portgate (the policy) and the sidecar's request directory, but not the request in it, so that a later
# install starts with every port closed instead of applying it again. Fails if a rule marked mtx-portgate: is left.
set -euo pipefail
[[ $(id -u) -eq 0 ]] || { echo "uninstall.sh: run as root" >&2; exit 1; }
# Nothing starts the helper any more; close-all then waits for a run already going (they share a lock), and a run
# started before that and applied after it opens nothing (a close-all holds until the sidecar has taken it in).
systemctl disable --now mtx-portgate.path mtx-portgate.timer 2>/dev/null || true
if [[ -x /usr/local/sbin/mtx-portgate ]]; then
  /usr/local/sbin/mtx-portgate close-all >/dev/null
fi
desired=$(sed -n 's/.*"desired": *"\([^"]*\)".*/\1/p' /etc/mtx-portgate/policy.json 2>/dev/null || true)
rm -f "${desired:-/var/lib/mtx-portgate-requests/desired.json}"
rm -f /etc/systemd/system/mtx-portgate.{service,path,timer} /usr/local/sbin/mtx-portgate
systemctl daemon-reload
rm -rf /var/lib/mtx-portgate
left=$(ufw show added | grep -c "mtx-portgate:" || true)
if [[ $left -ne 0 ]]; then
  echo "uninstall.sh: ufw still has $left rule(s) marked mtx-portgate: (sudo ufw show added); delete them by hand" >&2
  exit 1
fi
echo "mtx-portgate removed; ufw has no mtx-portgate: rules left"
