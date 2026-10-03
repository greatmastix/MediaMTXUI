#!/usr/bin/env bash
# install.sh: installs MediaMTX UI on a Linux machine, from nothing to a running setup page. Installs Docker if it
# is missing, asks a few questions (or takes them as options), downloads the release's files into an install folder,
# starts the stack and prints the address and the setup token. Run it again to upgrade to the newest release.
#
#   curl -fsSL https://github.com/greatmastix/MediaMTXUI/releases/latest/download/install.sh | sudo bash
#
# Options (all optional; without them it asks):
#   --public DOMAIN     on the internet at https://DOMAIN, with a certificate from Let's Encrypt
#   --email ADDRESS     where Let's Encrypt sends expiry warnings (public only)
#   --staging           Let's Encrypt's staging server, for a first try (public only)
#   --local [ADDRESS]   on a local network only, at http://ADDRESS:8080 (default: this machine's LAN address)
#   --dir PATH          the install folder (default /opt/mediamtx-ui)
#   --version vX.Y.Z    a particular release instead of the newest
#   --yes               no questions: take the defaults, or fail where there is none
#   --help
set -euo pipefail

REPO=greatmastix/MediaMTXUI
DIR=/opt/mediamtx-ui
MODE="" DOMAIN="" EMAIL="" STAGING="" LAN="" VERSION="" YES=""
# MTXUI_INSTALL_BASE: where the release files come from (tests point it at a checkout's files).
BASE=${MTXUI_INSTALL_BASE:-}

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
ok() { printf '  \033[32m✔\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*" >&2; }
die() {
  printf '\n\033[31mError:\033[0m %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Installs MediaMTX UI, or upgrades an existing install. Without options it asks.

  --public DOMAIN     on the internet at https://DOMAIN, with a certificate from Let's Encrypt
  --email ADDRESS     where Let's Encrypt sends expiry warnings (public only)
  --staging           Let's Encrypt's staging server, for a first try (public only)
  --local [ADDRESS]   on a local network only, at http://ADDRESS:8080 (default: this machine's LAN address)
  --dir PATH          the install folder (default /opt/mediamtx-ui)
  --version vX.Y.Z    a particular release instead of the newest
  --yes               no questions: take the defaults, or fail where there is none
EOF
}

while [[ $# -gt 0 ]]; do
  case $1 in
    --public)
      MODE=public DOMAIN=${2:-}
      shift
      ;;
    --email)
      EMAIL=${2:-}
      shift
      ;;
    --staging) STAGING=1 ;;
    --local)
      MODE=local
      if [[ ${2:-} != "" && ${2:-} != --* ]]; then
        LAN=$2
        shift
      fi
      ;;
    --dir)
      DIR=${2:-}
      shift
      ;;
    --version)
      VERSION=${2:-}
      shift
      ;;
    --yes | -y) YES=1 ;;
    --help | -h)
      usage
      exit 0
      ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
  shift
done

# Questions read the terminal, also when the script itself arrives through a pipe (curl ... | sudo bash).
TTY=""
if [[ -z $YES ]] && { exec 3</dev/tty; } 2>/dev/null; then TTY=1; fi
ask() { # ask VAR "question" [default]
  local var=$1 q=$2 def=${3:-} answer
  if [[ -z $TTY ]]; then
    [[ -n $def ]] || die "$q: no answer (run it in a terminal, or pass the option: see --help)"
    printf -v "$var" '%s' "$def"
    return
  fi
  if [[ -n $def ]]; then printf '  %s [%s]: ' "$q" "$def"; else printf '  %s: ' "$q"; fi
  IFS= read -r answer <&3 || true
  printf -v "$var" '%s' "${answer:-$def}"
}
yesno() { # yesno "question" default(y|n)
  local answer
  ask answer "$1 (y/n)" "$2"
  [[ $answer == [yY]* ]]
}

# --- 1. The machine ------------------------------------------------------------------------------------------------

bold "MediaMTX UI installer"
[[ $(uname -s) == Linux ]] || die "this installer is for Linux. On Windows or macOS, see docs/install-local.md (Docker Desktop)."
[[ $(id -u) -eq 0 ]] || die "run it as root: put sudo in front (curl ... | sudo bash)."
case $(uname -m) in
  x86_64 | aarch64 | arm64 | armv7l) ;;
  *) die "this machine's architecture ($(uname -m)) has no image; amd64, arm64 and armv7 do." ;;
esac
# shellcheck disable=SC1091 # the machine's own file; read in a subshell, since it sets VERSION too
os=$(. /etc/os-release 2>/dev/null && echo "${PRETTY_NAME:-}")
ok "${os:-Linux} on $(uname -m)"

need_pkg() { # need_pkg command package
  command -v "$1" >/dev/null && return
  info "Installing $2..."
  if command -v apt-get >/dev/null; then
    DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$2" >/dev/null 2>&1
  elif command -v dnf >/dev/null; then
    dnf install -y -q "$2" >/dev/null
  else
    die "please install $2 first."
  fi
}
need_pkg curl curl

# --- 2. Docker -----------------------------------------------------------------------------------------------------

if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo installed), $(docker compose version --short 2>/dev/null | sed 's/^/Compose /')"
else
  if command -v docker >/dev/null; then
    warn "Docker is installed, but without the Compose plugin (docker compose)."
  fi
  info "Installing Docker with Docker's install script (get.docker.com). This takes a minute or two."
  curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
  sh /tmp/get-docker.sh >/tmp/get-docker.log 2>&1 || die "Docker's install script failed; its output is in /tmp/get-docker.log."
  rm -f /tmp/get-docker.sh
  docker compose version >/dev/null 2>&1 || die "Docker is installed, but docker compose does not work; see /tmp/get-docker.log."
  ok "Docker installed"
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker info >/dev/null 2>&1 || die "Docker is installed but not running (systemctl status docker says why)."

# --- 3. Where the files come from ----------------------------------------------------------------------------------

if [[ -z $BASE ]]; then
  if [[ -n $VERSION ]]; then
    [[ $VERSION == v* ]] || VERSION=v$VERSION
    BASE=https://github.com/$REPO/releases/download/$VERSION
  else
    BASE=https://github.com/$REPO/releases/latest/download
  fi
fi
fetch() { # fetch release-file destination
  curl -fsSL "$BASE/$1" -o "$2.new" || {
    rm -f "$2.new"
    die "could not download $1 from $BASE (is the machine online?)"
  }
  mv "$2.new" "$2"
}

# --- 4. Upgrade an existing install --------------------------------------------------------------------------------

if [[ -f $DIR/.env && -f $DIR/compose.yaml ]]; then
  bold "Upgrading the install in $DIR"
  cd "$DIR"
  fetch compose.yaml compose.yaml
  # The overrides this install uses (named in COMPOSE_FILE) come from the same release.
  for f in compose.lan.yaml compose.behind-proxy.yaml compose.exposure.yaml; do
    if [[ -f $f ]]; then fetch "$f" "$f"; fi
  done
  ok "Downloaded the release's compose files (your .env is kept)"
  info "Downloading the images and restarting..."
  docker compose pull -q
  docker compose up -d --wait --wait-timeout 180 >/dev/null || die "the stack did not come up; docker compose logs (in $DIR) says why."
  ok "Upgraded and running"
  info "Back up first next time: Backups page, \"Back up now\". Old images: docker image prune"
  exit 0
fi

# --- 5. Questions --------------------------------------------------------------------------------------------------

lan_ip() {
  local ip
  ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)
  [[ -n $ip ]] || ip=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^172\.1[7-9]\.' | head -1)
  printf '%s' "$ip"
}
public_ip() { curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true; }

if [[ -z $MODE ]]; then
  echo
  bold "How will people reach it?"
  info "1) On the internet, at a domain name (https://stream.example.com): needs a domain that points here,"
  info "   and ports 80 and 443 open. Gets its certificate from Let's Encrypt by itself."
  info "2) On this local network only (http://$(lan_ip):8080): no domain, nothing open to the internet."
  choice=""
  ask choice "Choose 1 or 2" ""
  case $choice in
    1) MODE=public ;;
    2) MODE=local ;;
    *) die "please answer 1 or 2." ;;
  esac
fi

if [[ $MODE == public ]]; then
  [[ -n $DOMAIN ]] || ask DOMAIN "The domain name (e.g. stream.example.com)" ""
  DOMAIN=${DOMAIN#https://}
  DOMAIN=${DOMAIN#http://}
  DOMAIN=${DOMAIN%%/*}
  [[ $DOMAIN =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z]{2,}$ ]] || die "\"$DOMAIN\" is not a domain name."
  [[ -n $EMAIL || -n $YES ]] || ask EMAIL "E-mail for Let's Encrypt's expiry warnings (optional, Enter to skip)" ""
  # The A record must point here, or Let's Encrypt cannot check the domain.
  resolved=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1 {print $1}')
  mine=$(public_ip)
  if [[ -z $resolved ]]; then
    warn "$DOMAIN does not resolve yet. Create an A record pointing at ${mine:-the public IP of this server} (docs/before-you-start.md)."
    yesno "Continue anyway? The certificate will come once the record works" n || exit 1
  elif [[ -n $mine && $resolved != "$mine" ]]; then
    warn "$DOMAIN points at $resolved, but this server's public IP is $mine."
    warn "Fine if a router forwards ports 80 and 443 here; otherwise fix the A record first."
    yesno "Continue?" n || exit 1
  else
    ok "$DOMAIN points at this server ($resolved)"
  fi
  for port in 80 443; do
    if ss -Hltn "sport = :$port" 2>/dev/null | grep -q .; then
      die "port $port is already in use by another program (ss -ltnp shows which). For a web server already running here, see docs/behind-a-proxy.md."
    fi
  done
else
  MODE=local
  [[ -n $LAN ]] || ask LAN "This machine's address on the network" "$(lan_ip)"
  [[ -n $LAN ]] || die "no address: pass it with --local ADDRESS."
  if ss -Hltn "sport = :8080" 2>/dev/null | grep -q .; then
    die "port 8080 is already in use by another program (ss -ltnp shows which)."
  fi
fi
for port in 1935 8554; do
  if ss -Hltn "sport = :$port" 2>/dev/null | grep -q .; then
    die "stream port $port is already in use (another MediaMTX?). ss -ltnp shows which program."
  fi
done

# --- 6. Files ------------------------------------------------------------------------------------------------------

echo
bold "Installing into $DIR"
mkdir -p "$DIR"
cd "$DIR"
fetch compose.yaml compose.yaml
fetch env.example .env
chmod 600 .env
set_env() { # set_env KEY VALUE: replaces "KEY=" or "# KEY=" in .env, or appends it
  if grep -qE "^#? ?$1=" .env; then
    sed -i -E "s|^#? ?$1=.*|$1=$2|" .env
  else
    printf '%s=%s\n' "$1" "$2" >>.env
  fi
}
if [[ $MODE == public ]]; then
  set_env DOMAIN "$DOMAIN"
  set_env ACME_EMAIL "$EMAIL"
  [[ -z $STAGING ]] || set_env ACME_DIRECTORY https://acme-staging-v02.api.letsencrypt.org/directory
  URL=https://$DOMAIN
else
  fetch compose.lan.yaml compose.lan.yaml
  set_env DOMAIN "$LAN"
  set_env COMPOSE_FILE compose.yaml:compose.lan.yaml
  URL=http://$LAN:8080
fi
ok "compose.yaml and .env written"

# --- 7. Start ------------------------------------------------------------------------------------------------------

info "Downloading the images and starting (the first time takes a minute or two)..."
docker compose pull -q
docker compose up -d --wait --wait-timeout 180 >/dev/null || die "the stack did not come up; run 'docker compose logs' in $DIR to see why."
ok "Running"

if [[ $MODE == public ]]; then
  # The first HTTPS request makes the sidecar fetch its certificate.
  info "Getting the certificate from Let's Encrypt..."
  got=""
  for _ in $(seq 1 12); do
    if curl -fsS --max-time 10 --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/api/v1/health" >/dev/null 2>&1; then
      got=1
      break
    fi
    sleep 5
  done
  if [[ -n $got ]]; then
    ok "Certificate issued"
  elif [[ -n $STAGING ]]; then
    ok "Staging certificate requested (browsers will warn about it)"
  else
    warn "No certificate yet. Usually ports 80 and 443 are not reachable from the internet (a cloud firewall or the"
    warn "router), or the A record is not live yet. 'docker compose logs sidecar' in $DIR says why; it retries by itself."
  fi
fi

TOKEN=$(docker compose exec -T sidecar /mtxui setup-token 2>/dev/null || true)

# docker without sudo, for the person who ran this (the docker group is as powerful as root).
if [[ -n ${SUDO_USER:-} && $SUDO_USER != root ]] && ! id -nG "$SUDO_USER" | grep -qw docker; then
  if [[ -n $TTY ]] && yesno "Let $SUDO_USER run docker commands without sudo (adds them to the docker group)?" y; then
    usermod -aG docker "$SUDO_USER" && ok "$SUDO_USER is in the docker group (log out and in again for it to apply)"
  fi
fi

echo
bold "Done. Open $URL"
if [[ -n $TOKEN ]]; then info "Setup token: $TOKEN"; fi
info "Install folder: $DIR (run docker compose commands there)"
if [[ $MODE == public ]]; then
  info "Stream ports to open in your firewall when you use them: 1935/tcp RTMP, 8554/tcp RTSP, 8890/udp SRT,"
  info "8189/udp WebRTC (watching in browsers)."
fi
info "Upgrade later by running this installer again. Docs: https://github.com/$REPO/tree/main/docs"
