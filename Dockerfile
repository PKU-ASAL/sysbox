# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

COPY . .

# Build sysbox and sysbox-init.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/sysbox ./cmd/sysbox
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/sysbox-init ./cmd/sysbox-init

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# Install ca-certificates first (needed for HTTPS apt sources),
# then switch to tuna mirror and install runtime deps.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    sed -i 's|http://|https://|g; s|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apt/sources.list.d/debian.sources 2>/dev/null || \
    sed -i 's|http://|https://|g; s|deb.debian.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apt/sources.list 2>/dev/null || true && \
    apt-get update && apt-get install -y --no-install-recommends \
    iproute2 \
    iptables \
    iputils-ping \
    qemu-kvm \
    libvirt-clients \
    docker.io \
    e2fsprogs \
    util-linux \
    openssh-client \
    sshpass \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/sysbox       /usr/local/bin/sysbox
COPY --from=builder /out/sysbox-init  /usr/local/bin/sysbox-init

# Default directories inside container.
RUN mkdir -p /workspaces /runs /var/cache/sysbox

EXPOSE 9876

ENTRYPOINT ["sysbox", "serve"]
