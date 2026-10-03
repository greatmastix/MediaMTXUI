# Behind your own reverse proxy

If a reverse proxy already terminates HTTPS on the host (Caddy, nginx, Traefik), let it do that for MediaMTX UI too:
the sidecar then serves plain HTTP on `127.0.0.1:8080`, and gets no certificate of its own. Download the override
next to `compose.yaml`:

```bash
curl -fsSLO https://github.com/greatmastix/MediaMTXUI/releases/latest/download/compose.behind-proxy.yaml
```

add this line to `.env`, so that every `docker compose` command (upgrades included) uses both files:

```bash
COMPOSE_FILE=compose.yaml:compose.behind-proxy.yaml
```

and run `docker compose up -d`. (In a checkout of the repository, the override is
`deploy/compose.behind-proxy.yaml`.) In `.env`, `DOMAIN` is still the name users open; `UI_PORT` changes the local
port. The stream ports are published as before: the proxy only carries the UI, its API, and the browser's live view
(HLS and WebRTC signalling); WebRTC media (UDP 8189) goes straight to MediaMTX.

## What the proxy must do

- Pass the `Host` header, and set `X-Forwarded-For` and `X-Forwarded-Proto`.
- Not buffer responses: the live event stream and log tail are long-lived (server-sent events).
- Allow large request bodies for holding clips (64 MB) and backup uploads (up to `MTXUI_BACKUP_MAX_UPLOAD_MB`).

The sidecar believes `X-Forwarded-*` only from `TRUSTED_PROXIES`. With the proxy on the same host, connections reach
the container from the stack network's gateway: the override pins the network to `172.29.42.0/24` and trusts
`172.29.42.1`. If that subnet is taken on your host, set `MTX_SUBNET` and `TRUSTED_PROXIES` in `.env`. The dashboard
warns when requests arrive in a way that does not match `DOMAIN` (wrong host, plain HTTP).

## Caddy

```
stream.example.com {
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
	}
	request_body {
		max_size 2GB
	}
}
```

## nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name stream.example.com;
    # ssl_certificate ... (certbot, or your own)

    client_max_body_size 2g;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

## Traefik

Traefik reaches the sidecar on a shared Docker network instead of a published port. Build on the override above, and
add one of your own next to it, `compose.traefik.yaml`:

```yaml
services:
  sidecar:
    ports: !reset []
    networks: [mtx, traefik]
    labels:
      traefik.enable: "true"
      traefik.docker.network: traefik
      traefik.http.routers.mtxui.rule: Host(`stream.example.com`)
      traefik.http.routers.mtxui.tls.certresolver: letsencrypt
      traefik.http.services.mtxui.loadbalancer.server.port: "9080"

networks:
  traefik:
    external: true
```

In `.env`, list all three files, and trust Traefik:

```bash
COMPOSE_FILE=compose.yaml:compose.behind-proxy.yaml:compose.traefik.yaml
# Traefik's address on the traefik network (docker network inspect traefik)
TRUSTED_PROXIES=172.30.0.2
```

The behind-proxy override matters here: on two networks, the sidecar cannot tell from its own interfaces which one
is the stack's, so the override pins the stack network to `MTX_SUBNET` and names it to the sidecar
(`MTXUI_STACK_SUBNET`). MediaMTX believes the client addresses the sidecar passes on only from that subnet. Were the
Traefik network taken for it, live view in the UI would be refused, and MediaMTX would see every client of `/whip/`
and `/whep/` as the sidecar itself. `traefik.docker.network` tells Traefik which of the sidecar's two networks to
use.
