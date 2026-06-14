# syntax=docker/dockerfile:1.7

# Builder — version MUST match the `go` directive in go.mod and the `go` pin in
# .mise.toml (enforced by `make check-go-alignment`).
FROM golang:1.26.4-bookworm AS build

WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64

# CGO disabled → fully static binaries for the distroless/static runtime.
# No cgo dependencies are in use (no SQLite/confluent-kafka/etc.), so the
# static base is safe — verified by `make e2e`/run, not just build.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/main.go && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

# Runtime — distroless static, runs as the non-root `nonroot` user (UID 65532).
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

ARG APP_INTERNAL_PORT=7999
ENV APP_PORT=${APP_INTERNAL_PORT} \
    HEALTHCHECK_HOST=localhost

COPY --from=build /out/app /app
COPY --from=build /out/healthcheck /healthcheck

EXPOSE ${APP_INTERNAL_PORT}

USER nonroot:nonroot

# Distroless has no shell/curl — the probe is a compiled binary. HEALTHCHECK
# flag timings are parsed at build time and are NOT variable-expanded, so they
# are literal durations; the probe binary reads APP_PORT/HEALTHCHECK_HOST at run
# time.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/healthcheck"]

ENTRYPOINT ["/app"]
