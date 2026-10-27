#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
modcache="$(go env GOPATH)/pkg/mod"
image="${SYSBOX_GO_TEST_IMAGE:-golang:1.26-alpine}"
common=(--rm --privileged --pid=host -v "${root}:/src" -v "${modcache}:/go/pkg/mod:ro" -w /src -e GOPROXY=off -e GOCACHE=/tmp/go-build)

docker run "${common[@]}" "${image}" \
	go test -count=1 -tags e2e -v -run '^Test(OwnedPolicy.*|RootBridgeProxyLifecycle)E2E$' ./pkg/provider/network

docker run "${common[@]}" -v /var/run/docker.sock:/var/run/docker.sock "${image}" \
	go test -count=1 -tags e2e -v -run '^TestDocker(OwnedPolicyLifecycle|StartPreservesNamedNetworkNamespace)E2E$' ./pkg/provider/docker

make -C "${root}" test-firewall-recovery-e2e
