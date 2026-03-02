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

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw

# Switch to non-root user
USER picoclaw

# Run onboard to create initial directories and config
RUN /usr/local/bin/picoclaw onboard

ENTRYPOINT ["/usr/local/bin/picoclaw-entrypoint.sh"]
CMD ["gateway"]
