# Behind your own reverse proxy

If a reverse proxy already terminates HTTPS on the host (Caddy, nginx, Traefik), let it do that for MediaMTX UI too:
the sidecar then serves plain HTTP on `127.0.0.1:8080`, and gets no certificate of its own.

```bash
docker compose -f compose.yaml -f deploy/compose.behind-proxy.yaml up -d
```

(`deploy/compose.behind-proxy.yaml` is in the repository; download it next to `compose.yaml`.) In `.env`, `DOMAIN` is
still the name users open; `UI_PORT` changes the local port. The stream ports are published as before: the proxy
only carries the UI, its API, and the browser's live view (HLS and WebRTC signalling); WebRTC media (UDP 8189) goes
straight to MediaMTX.

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

Add labels to the `sidecar` service in a compose override of your own, and leave out its `ports:` (Traefik reaches it
on a shared Docker network instead):

```yaml
services:
  sidecar:
    environment:
      MTXUI_TLS: "off"
      MTXUI_TRUSTED_PROXIES: <Traefik's address on the shared network>
    ports: !reset []
    networks: [mtx, traefik]
    labels:
      traefik.enable: "true"
      traefik.http.routers.mtxui.rule: Host(`stream.example.com`)
      traefik.http.routers.mtxui.tls.certresolver: letsencrypt
      traefik.http.services.mtxui.loadbalancer.server.port: "9080"

networks:
  traefik:
    external: true
```
