#!/usr/bin/env bash
# Usage: scripts/compose-assert.sh <compose-project> <check>
#   ports-dev   every port the project's containers publish is bound to 127.0.0.1 on host port 18000-18999
#   ports-none  the project's containers publish nothing
#   gone        no containers, networks or volumes of the project are left
#   hardening   every running container is non-root, drops all capabilities, cannot gain privileges, has memory and
#               pid limits, does not mount the Docker socket, and (except the dev tooling) has a read-only root
set -euo pipefail

usage="usage: compose-assert.sh <compose-project> ports-dev|ports-none|gone|hardening"
project=${1:?$usage}
check=${2:?$usage}
filter="label=com.docker.compose.project=$project"
fail=0

# Both the requested bindings (HostConfig) and the effective ones (NetworkSettings) are checked.
# shellcheck disable=SC2016 # docker inspect Go templates, not shell expansions
bindings='{{range $p, $b := .HostConfig.PortBindings}}{{range $b}}{{$p}}|{{.HostIp}}|{{.HostPort}}{{"\n"}}{{end}}{{end}}'\
'{{range $p, $b := .NetworkSettings.Ports}}{{range $b}}{{$p}}|{{.HostIp}}|{{.HostPort}}{{"\n"}}{{end}}{{end}}'

case $check in
  ports-dev | ports-none)
    containers=$(docker ps -q --filter "$filter")
    if [[ -z $containers ]]; then
      echo "compose-assert: no running containers in project $project" >&2
      exit 1
    fi
    for c in $containers; do
      name=$(docker inspect -f '{{.Name}}' "$c")
      while IFS='|' read -r port ip hostport; do
        [[ -n $port ]] || continue
        where="${ip:-0.0.0.0}:${hostport:-?}"
        if [[ $check == ports-none ]]; then
          echo "compose-assert: $name publishes $port on $where; project $project must publish nothing" >&2
          fail=1
        elif [[ $ip != 127.0.0.1 || ! $hostport =~ ^18[0-9]{3}$ ]]; then
          echo "compose-assert: $name publishes $port on $where; dev stacks may only use 127.0.0.1:18000-18999" >&2
          fail=1
        fi
      done < <(docker inspect -f "$bindings" "$c")
    done
    ;;
  hardening)
    containers=$(docker ps -q --filter "$filter")
    if [[ -z $containers ]]; then
      echo "compose-assert: no running containers in project $project" >&2
      exit 1
    fi
    # shellcheck disable=SC2016 # docker inspect Go templates, not shell expansions
    tmpl='{{index .Config.Labels "com.docker.compose.service"}}|{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|'\
'{{.HostConfig.Privileged}}|{{.HostConfig.NetworkMode}}|{{json .HostConfig.CapDrop}}|{{json .HostConfig.CapAdd}}|'\
'{{json .HostConfig.SecurityOpt}}|{{.HostConfig.Memory}}|{{json .HostConfig.PidsLimit}}|{{range .Mounts}}{{.Source}} {{end}}'
    for c in $containers; do
      IFS='|' read -r svc user ro priv net capdrop capadd secopt mem pids mounts < <(docker inspect -f "$tmpl" "$c")
      bad=()
      [[ -n $user && ! $user =~ ^(0|root)(:|$) ]] || bad+=("runs as root")
      [[ $ro == true || $svc == web || $svc == playwright ]] || bad+=("writable root filesystem")
      [[ $priv == false ]] || bad+=("privileged")
      [[ $net != host ]] || bad+=("host network")
      [[ $capdrop == *'"ALL"'* ]] || bad+=("capabilities not dropped")
      [[ $capadd == null || $capadd == '[]' ]] || bad+=("adds capabilities $capadd")
      [[ $secopt == *no-new-privileges* ]] || bad+=("may gain privileges")
      [[ $mem =~ ^[1-9][0-9]*$ ]] || bad+=("no memory limit")
      [[ $pids =~ ^[1-9][0-9]*$ ]] || bad+=("no pids limit")
      [[ $mounts != *docker.sock* ]] || bad+=("mounts the Docker socket")
      if ((${#bad[@]})); then
        echo "compose-assert: $svc: ${bad[*]}" >&2
        fail=1
      fi
    done
    ;;
  gone)
    for kind in container network volume; do
      case $kind in
        container) left=$(docker ps -aq --filter "$filter") ;;
        network) left=$(docker network ls -q --filter "$filter") ;;
        volume) left=$(docker volume ls -q --filter "$filter") ;;
      esac
      if [[ -n $left ]]; then
        echo "compose-assert: project $project left ${kind}s behind: $(echo "$left" | tr '\n' ' ')" >&2
        fail=1
      fi
    done
    ;;
  *)
    echo "compose-assert: unknown check '$check'" >&2
    exit 2
    ;;
esac

if ((fail)); then exit 1; fi
echo "compose-assert: $project $check ok"
