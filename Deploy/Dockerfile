# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /out/oktapod ./Src/cmd/oktapod

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates sqlite3 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/oktapod /app/oktapod
ENV OKTAPOD_LISTEN_ADDRESS=0.0.0.0:8787 \
    OKTAPOD_PROFILE=team \
    OKTAPOD_DATA_ROOT=/data
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/app/oktapod", "serve"]
