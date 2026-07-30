# syntax=docker/dockerfile:1.7

FROM node:22-bookworm-slim AS web
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY Src/web/package.json Src/web/package.json
RUN pnpm install --frozen-lockfile
COPY Src/web Src/web
RUN pnpm --dir Src/web build

FROM golang:1.26-bookworm AS build
ARG OKTAPOD_VERSION=1.0.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY Src Src
COPY --from=web /src/Src/web/dist Src/web/dist
RUN CGO_ENABLED=1 go build -trimpath \
    -ldflags="-s -w -X github.com/oktapod/oktapod/Src/internal/application.RuntimeVersion=${OKTAPOD_VERSION}" \
    -o /out/oktapod ./Src/cmd/oktapod

FROM debian:bookworm-slim
ARG OKTAPOD_VERSION=1.0.0
LABEL org.opencontainers.image.title="Oktapod" \
    org.opencontainers.image.version="${OKTAPOD_VERSION}" \
    org.opencontainers.image.source="https://github.com/Water1209/Oktapod"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl sqlite3 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system oktapod \
    && useradd --system --gid oktapod --home-dir /app --shell /usr/sbin/nologin oktapod \
    && mkdir -p /data /app \
    && chown -R oktapod:oktapod /data /app
WORKDIR /app
COPY --from=build --chown=oktapod:oktapod /out/oktapod /app/oktapod
USER oktapod
ENV OKTAPOD_LISTEN_ADDRESS=0.0.0.0:8787 \
    OKTAPOD_PROFILE=personal \
    OKTAPOD_DATA_ROOT=/data
VOLUME /data
EXPOSE 8787
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
    CMD curl --fail --silent http://127.0.0.1:8787/api/v1/health >/dev/null || exit 1
ENTRYPOINT ["/app/oktapod", "serve"]
