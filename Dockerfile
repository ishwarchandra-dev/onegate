# syntax=docker/dockerfile:1

# OneGate — multi-stage single-binary image (p9.docker).
#
#   1. web:   dashboard SPA build (bun) + gzip precompression
#   2. go:    static gateway build (CGO_ENABLED=0, dashboard embedded)
#   3. final: distroless/static — CA certs + tzdata only, non-root,
#             writable /data volume, /healthz for orchestrators
#
# Build args:
#   VERSION / COMMIT / DATE — stamped into the binary (see Makefile LDFLAGS)

# --- Stage 1: dashboard build ---------------------------------------------
FROM oven/bun:1.4-slim AS web
WORKDIR /src/web
# Layer cache: dependencies first.
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
# Sources (embedded into the binary in stage 2).
COPY web/ ./
# SPA build (ADR 006) + build-time gzip siblings (same recipe as `make web`).
RUN bun run build \
 && find build/client -type f \
      ! -name '*.gz' ! -name '*.woff2' ! -name '*.png' ! -name '*.ico' \
      -exec gzip -9 -k {} \;

# --- Stage 2: gateway build ------------------------------------------------
FROM golang:1.26-alpine AS go
WORKDIR /src
# Layer cache: modules first.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
# The built dashboard from stage 1 replaces the committed placeholder.
COPY --from=web /src/web/build/client web/build/client
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "\
-X github.com/ishwarchandra-dev/onegate/internal/version.Version=${VERSION} \
-X github.com/ishwarchandra-dev/onegate/internal/version.GitCommit=${COMMIT} \
-X github.com/ishwarchandra-dev/onegate/internal/version.BuildDate=${DATE}" \
      -o /out/onegate ./cmd/onegate \
 && ONEGATE_REQUIRE_EMBED=1 go test ./cmd/onegate \
      -run TestEmbeddedDashboardNotPlaceholder -count=1

# --- Stage 3: minimal runtime -----------------------------------------------
# distroless/static: no shell, no package manager, CA certificates and
# tzdata included (HTTPS upstreams work); runs as uid 65532 (nonroot).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go --chown=nonroot:nonroot /out/onegate /onegate
# USER before WORKDIR so /data is created owned by nonroot (writable
# volume target); the placeholder-refusal test already guaranteed the
# embedded dashboard is the real build.
USER nonroot
WORKDIR /data
VOLUME ["/data"]
EXPOSE 7420
ENTRYPOINT ["/onegate"]
# Sensible defaults: data in the volume, all interfaces, standard port.
CMD ["-data-dir", "/data", "-host", "0.0.0.0", "-port", "7420"]
