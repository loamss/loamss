# Loamss runtime — single-binary container for Cloud Run / Fly / GKE.
#
# Three-stage build:
#
#   1. console-builder — Next.js static export. Produces console/out/
#      which becomes the embedded asset bundle.
#   2. runtime-builder — Go compile with the console bundle stamped in
#      via //go:embed. Produces a single static binary.
#   3. runtime         — minimal distroless image carrying just the
#      binary, a CA bundle (for outbound HTTPS to model providers,
#      OAuth endpoints, etc.), and tzdata (for the audit log's local
#      timestamp formatting). ~30 MB image, no shell.
#
# Build:
#   docker build -t loamss:dev .
#
# Run locally (laptop equivalent):
#   docker run --rm -p 7777:7777 \
#     -v $HOME/.loamss:/data \
#     -e LOAMSS_DATA_DIR=/data \
#     loamss:dev
#
# Run as cloud (gate active, Postgres backing):
#   docker run --rm -p 8080:8080 \
#     -e PORT=8080 \
#     -e LOAMSS_PROFILE=cloud \
#     -e LOAMSS_DATABASE_URL=postgres://... \
#     -e LOAMSS_AUDIT_DATABASE_URL=postgres://... \
#     -e LOAMSS_SETUP_TOKEN=$(openssl rand -hex 32) \
#     loamss:dev
#
# Multi-arch (linux/amd64 + linux/arm64):
#   docker buildx build --platform linux/amd64,linux/arm64 -t loamss:dev .

# --- stage 1: console ----------------------------------------------------
FROM oven/bun:1.3-alpine AS console-builder

WORKDIR /src/console

# Cache bun install across console-source edits. The lockfile + package.json
# rarely change; the source under src/ does.
COPY console/package.json console/bun.lock* ./
RUN bun install --frozen-lockfile

COPY console/ ./
# Next.js static export → ./out
RUN bun run build

# --- stage 2: go binary --------------------------------------------------
FROM golang:1.25-alpine AS runtime-builder

# Build deps. git for go-mod version embedding via -ldflags;
# ca-certificates for the build-time fetches; tzdata so the embedded
# zoneinfo is available during go test if invoked.
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src/runtime

# Cache the module graph before pulling source code in. Touching a .go
# file under runtime/internal/ won't bust this layer.
COPY runtime/go.mod runtime/go.sum ./
RUN go mod download

COPY runtime/ ./

# Drop the console bundle into the embed path. The Makefile usually
# does this; we replicate the move here because Docker can't share
# state with the host's filesystem.
COPY --from=console-builder /src/console/out ./internal/console/dist

# Static binary. CGO=0 works because modernc.org/sqlite is pure Go.
# Multi-arch: TARGETARCH is set by buildx; defaults to the build host's
# arch for plain `docker build`.
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH

RUN go build \
    -trimpath \
    -ldflags " \
        -s -w \
        -X github.com/loamss/loamss/runtime/internal/cli.version=$VERSION \
        -X github.com/loamss/loamss/runtime/internal/cli.commit=$COMMIT \
        -X github.com/loamss/loamss/runtime/internal/cli.buildDate=$BUILD_DATE \
    " \
    -o /out/loamss \
    ./cmd/loamss

# --- stage 3: runtime image ---------------------------------------------
# Distroless static — no shell, no package manager. The runtime binary
# is fully static (CGO=0) so glibc isn't needed; distroless gives us a
# CA bundle, tzdata, and /etc/passwd with a non-root user out of the
# box. ~2 MB base.
FROM gcr.io/distroless/static-debian12:nonroot

# Copy the compiled binary.
COPY --from=runtime-builder /out/loamss /usr/local/bin/loamss

# Default working directory. Cloud Run / Fly mount no persistent volumes
# by default; the runtime's data_dir lives here ephemerally unless the
# operator points LOAMSS_DATABASE_URL / LOAMSS_AUDIT_DATABASE_URL at
# external Postgres (the cloud-ready path). For SQLite-on-volume setups
# (GKE PVCs, Fly volumes), mount over /data.
WORKDIR /data
USER nonroot:nonroot

# Cloud Run / Fly / GKE all pass the listener port via $PORT. Our entry
# command reads it via the profile + listen-addr resolution in start.go
# (see internal/cli/start.go).
EXPOSE 8080

# No Docker HEALTHCHECK: distroless/static has no shell, curl, or
# wget, and adding one would bloat the image. Cloud Run / Fly / GKE
# all do their own /healthz probes (which is always public, even
# with the setup-token gate active — see internal/server/setuptoken.go).
# Local `docker run` users can verify with:
#   curl http://localhost:8080/healthz

# Default entrypoint. Operators can override (e.g., `loamss audit log`)
# by passing arguments to docker run.
ENTRYPOINT ["/usr/local/bin/loamss"]
CMD ["start"]
