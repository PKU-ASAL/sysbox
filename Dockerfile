ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG SOURCE_URL=https://github.com/PKU-ASAL/sysbox
ARG RELEASE_FINGERPRINT=unknown

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

ARG VERSION
ARG REVISION
ARG CREATED

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

COPY . .

# Build sysbox and sysbox-init.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/oslab/sysbox/pkg/buildinfo.Version=${VERSION} -X github.com/oslab/sysbox/pkg/buildinfo.Commit=${REVISION} -X github.com/oslab/sysbox/pkg/buildinfo.BuildTime=${CREATED}" \
    -o /out/sysbox ./cmd/sysbox
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X github.com/oslab/sysbox/pkg/buildinfo.Version=${VERSION} -X github.com/oslab/sysbox/pkg/buildinfo.Commit=${REVISION} -X github.com/oslab/sysbox/pkg/buildinfo.BuildTime=${CREATED}" \
    -o /out/sysbox-init ./cmd/sysbox-init

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM debian:bookworm-slim

ARG VERSION
ARG REVISION
ARG CREATED
ARG SOURCE_URL
ARG RELEASE_FINGERPRINT

LABEL org.opencontainers.image.title="Sysbox" \
      org.opencontainers.image.description="Declarative control plane for heterogeneous Linux experiment topologies" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="MulanPSL-2.0" \
      io.github.pku-asal.sysbox.release-fingerprint="${RELEASE_FINGERPRINT}" \
      org.opencontainers.image.documentation="${SOURCE_URL}/blob/main/docs/index.md"

# The builder installs git and therefore has a current CA bundle. Reuse it to
# bootstrap the runtime image's first HTTPS apt transaction.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Use the HTTPS mirror before the first update. The Debian base image carries
# the bootstrap trust store; ca-certificates is then refreshed with the rest
# of the runtime dependencies.
RUN if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i 's|http://|https://|g; s|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g; s|security.debian.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apt/sources.list.d/debian.sources; \
    else \
      sed -i 's|http://|https://|g; s|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g; s|security.debian.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apt/sources.list; \
    fi && \
    apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    iproute2 \
    iptables \
    iputils-ping \
    qemu-kvm \
    qemu-utils \
    genisoimage \
    libvirt-clients \
    docker.io \
    e2fsprogs \
    util-linux \
    openssh-client \
    sshpass \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/sysbox       /usr/local/bin/sysbox
COPY --from=builder /out/sysbox-init  /usr/local/bin/sysbox-init

# Default service and artifact directories inside the container.
RUN mkdir -p /var/lib/sysbox/workspaces /var/lib/sysbox/runs /var/lib/sysbox/firecracker /var/cache/sysbox

EXPOSE 9876

ENTRYPOINT ["sysbox", "serve"]
