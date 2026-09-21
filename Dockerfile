# syntax=docker/dockerfile:1
# Two static binaries from one build: the server, and the demo-data generator.
# The Go version here must match the `go` directive in go.mod.
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/league ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/league-seed ./cmd/seed

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S league \
    && adduser -S -D -H -G league league \
    && mkdir -p /app/data \
    && chown -R league:league /app

WORKDIR /app
COPY --from=builder --chown=league:league /out/league /app/league
COPY --from=builder --chown=league:league /out/league-seed /app/league-seed
COPY --chown=league:league web /app/web
COPY --chown=league:league seed /app/seed
COPY --chown=league:league tools/docker-entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

USER league
VOLUME ["/app/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8080/healthz || exit 1

# ADDR and DB_PATH come from the environment (see .env.example).
# SEED=1 creates a demo database on first boot only.
ENV DB_PATH=/app/data/league.db
ENTRYPOINT ["/app/entrypoint.sh"]
