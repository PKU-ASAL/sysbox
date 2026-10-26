ARG BINARY_SOURCE=source
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

# Release images consume the verified archive through a named build context.
FROM builder AS binaries-source
FROM alpine:3.22 AS binaries-release
RUN apk add --no-cache jq ca-certificates
ARG TARGETARCH
ARG VERSION
ARG REVISION
ARG RELEASE_FINGERPRINT
COPY --from=release / /release/
RUN cd /release && sha256sum -c SHA256SUMS && \
    jq -e --arg revision "${REVISION}" --arg version "${VERSION}" --arg fingerprint "${RELEASE_FINGERPRINT}" \
      '.commit == $revision and .version == $version and .release_fingerprint == $fingerprint' build-metadata.json && \
    mkdir /out && tar -xzf "sysbox_${VERSION}_linux_${TARGETARCH}.tar.gz" -C /out && \
    jq -r --arg arch "${TARGETARCH}" '.targets[] | select(.architecture == $arch) | \
      "\(.sysbox_sha256)  /out/sysbox\n\(.sysbox_init_sha256)  /out/sysbox-init\n\(.sysbox_netns_sha256)  /out/sysbox-netns"' \
      build-metadata.json | sha256sum -c -
FROM binaries-${BINARY_SOURCE} AS binaries

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

# Reuse the build stage CA bundle to bootstrap runtime HTTPS package installs.
COPY --from=binaries /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

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

COPY --from=binaries /out/sysbox       /usr/local/bin/sysbox
COPY --from=binaries /out/sysbox-init  /usr/local/bin/sysbox-init

# Docker 29 daemons require client API >= 1.44; Debian's docker.io ships 20.10.
# Override its CLI with a modern one (dockerd stays available for test rigs).
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker

# sysbox-netns is the firewall's shim over nsenter+nft. It isolates each nft
# operation into a killable subprocess (with pdeathsig) so a wedged netfilter
# dump cannot hang the agent. The runtime container must grant the capabilities
# required by nsenter/nft; file capability reduction is a separate concern.
COPY --from=binaries /out/sysbox-netns /usr/local/bin/sysbox-netns

# Default service and artifact directories inside the container.
RUN mkdir -p /var/lib/sysbox/workspaces /var/lib/sysbox/runs /var/lib/sysbox/firecracker /var/cache/sysbox

EXPOSE 9876

ENTRYPOINT ["sysbox", "serve"]
