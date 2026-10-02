# Sidecar image: the web UI built with Node, embedded into a static Go binary, shipped on scratch as uid 10002, with
# the pinned MediaMTX binary alongside for `--validate-conf` only (every config is validated before it is written).
# Multi-arch (linux/amd64, arm64, arm/v7): the web and Go stages run on the build machine and cross-compile; only the
# MediaMTX binary is taken per target platform.
# The ARG defaults mirror deploy/tools.env (`./dev lint` fails if they drift), because plain
# `docker build .` uses them as they are.
ARG NODE_IMAGE=node:24.21.0-alpine3.24@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1
ARG MEDIAMTX_IMAGE=bluenviron/mediamtx:1.21.1@sha256:5ce2a948eb68df06ce2e13870db8df8e30d516ac4dc40e04bfe8aee3bdf7be40
ARG GO_IMAGE=golang:1.27.1-trixie@sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183

FROM ${MEDIAMTX_IMAGE} AS mediamtx

FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build && node scripts/bundle-budget.mjs dist

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS sidecar
WORKDIR /src/sidecar
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY sidecar/go.mod sidecar/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY sidecar/ ./
COPY --from=web /src/web/dist/ ./internal/webui/dist/
ARG VERSION=dev
ARG MEDIAMTX_VERSION=1.21.1
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    export GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
 && go build -trimpath -ldflags "-s -w \
      -X mtxui/internal/buildinfo.Version=${VERSION} \
      -X mtxui/internal/buildinfo.MediaMTXVersion=${MEDIAMTX_VERSION}" \
      -o /out/mtxui ./cmd/mtxui \
 && go build -trimpath -ldflags "-s -w" -o /out/mtx-portgate ./cmd/mtx-portgate
# Mount points owned by 10002, so empty named volumes take that ownership; /tmp for the validator's scratch files.
RUN mkdir -p /out/data/config /out/data/recordings /out/data/logs /out/data/state /out/data/hooks /out/data/portgate /out/data/portgate-status /out/data/backups /out/holding /out/tmp \
 && chmod 700 /out/data/state /out/data/backups && chmod 1777 /out/tmp

FROM scratch
COPY --from=sidecar /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=sidecar /out/mtxui /mtxui
COPY --from=mediamtx /mediamtx /usr/libexec/mtxui/mediamtx
# The host helper for exposure control, carried here so the host can take it from the image it runs
# (deploy/host/portgate/install.sh). The sidecar never runs it.
COPY --from=sidecar /out/mtx-portgate /usr/libexec/mtxui/mtx-portgate
COPY --from=sidecar --chown=10002:10002 /out/data /data
COPY --from=sidecar --chown=10002:10002 /out/holding /holding
COPY --from=sidecar /out/tmp /tmp
USER 10002:10002
EXPOSE 9080 9081
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/mtxui", "healthcheck"]
ENTRYPOINT ["/mtxui"]
CMD ["serve"]
