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

RUN apk add --no-cache git jq bash

WORKDIR /src
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

COPY . .

# Use the same build order as the release archives; never embed checkout helpers.
RUN bash scripts/release/build-binaries.sh /out \
    "-s -w -X github.com/oslab/sysbox/pkg/buildinfo.Version=${VERSION} -X github.com/oslab/sysbox/pkg/buildinfo.Commit=${REVISION} -X github.com/oslab/sysbox/pkg/buildinfo.BuildTime=${CREATED}"

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM docker:29-cli AS dockercli
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
    nftables \
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

# Docker 29 daemons require client API >= 1.44; Debian's docker.io ships 20.10.
# Override its CLI with a modern one (dockerd stays available for test rigs).
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker

# sysbox-netns is the firewall's shim over nsenter+nft. It isolates each nft
# operation into a killable subprocess (with pdeathsig) so a wedged netfilter
# dump cannot hang the agent. The runtime container must grant the capabilities
# required by nsenter/nft; file capability reduction is a separate concern.
COPY --from=builder /out/sysbox-netns /usr/local/bin/sysbox-netns

# Default service and artifact directories inside the container.
RUN mkdir -p /var/lib/sysbox/workspaces /var/lib/sysbox/runs /var/lib/sysbox/firecracker /var/cache/sysbox

EXPOSE 9876

ENTRYPOINT ["sysbox", "serve"]
