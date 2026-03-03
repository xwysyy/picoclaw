# ============================================================
# Stage 1: Build the picoclaw binary
# ============================================================
FROM golang:1.25 AS builder

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy

ENV HTTP_PROXY=$HTTP_PROXY \
  HTTPS_PROXY=$HTTPS_PROXY \
  NO_PROXY=$NO_PROXY \
  http_proxy=$http_proxy \
  https_proxy=$https_proxy \
  no_proxy=$no_proxy

RUN apt-get update && \
    apt-get install -y --no-install-recommends git make ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN make build

# ============================================================
# Stage 1b: Build Gateway Console UI (Next.js static export)
# ============================================================
FROM node:22-alpine AS console-builder

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy

ENV HTTP_PROXY=$HTTP_PROXY \
  HTTPS_PROXY=$HTTPS_PROXY \
  NO_PROXY=$NO_PROXY \
  http_proxy=$http_proxy \
  https_proxy=$https_proxy \
  no_proxy=$no_proxy \
  NEXT_TELEMETRY_DISABLED=1

WORKDIR /src

# Cache dependencies
COPY web/picoclaw-console/package.json web/picoclaw-console/package-lock.json ./web/picoclaw-console/
RUN cd web/picoclaw-console && npm ci

# Copy sources and build
COPY web/picoclaw-console ./web/picoclaw-console
RUN cd web/picoclaw-console && npm run build

# ============================================================
# Stage 2: Minimal runtime image
# ============================================================
FROM alpine:3.23

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ARG http_proxy
ARG https_proxy
ARG no_proxy

ENV HTTP_PROXY=$HTTP_PROXY \
  HTTPS_PROXY=$HTTPS_PROXY \
  NO_PROXY=$NO_PROXY \
  http_proxy=$http_proxy \
  https_proxy=$https_proxy \
  no_proxy=$no_proxy

RUN apk add --no-cache \
  ca-certificates \
  tzdata \
  curl \
  git \
  openssh-client \
  python3 \
  poppler-utils \
  imagemagick \
  texlive

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl --silent --fail --noproxy '*' http://localhost:18790/health >/dev/null || exit 1

# Copy binary
COPY --from=builder /src/build/picoclaw /usr/local/bin/picoclaw
COPY docker/entrypoint.sh /usr/local/bin/picoclaw-entrypoint.sh
RUN chmod +x /usr/local/bin/picoclaw-entrypoint.sh

# Copy Console UI static assets
COPY --from=console-builder /src/web/picoclaw-console/out /usr/local/share/picoclaw/console

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw

# Switch to non-root user
USER picoclaw

# NOTE: Do not run `picoclaw onboard` at build-time. In real deployments the
# config/workspace are typically mounted as volumes, and running onboard here
# bloats layers and can fail under low-disk CI environments. The entrypoint
# performs best-effort first-run onboarding when needed.

ENTRYPOINT ["/usr/local/bin/picoclaw-entrypoint.sh"]
CMD ["gateway"]
