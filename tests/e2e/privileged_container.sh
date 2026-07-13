#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
modcache="$(go env GOPATH)/pkg/mod"
image="${SYSBOX_GO_TEST_IMAGE:-golang:1.26-alpine}"
common=(--rm --privileged --pid=host -v "${root}:/src" -v "${modcache}:/go/pkg/mod:ro" -w /src -e GOPROXY=off -e GOCACHE=/tmp/go-build)

docker run "${common[@]}" "${image}" \
	go test -count=1 -tags e2e -v -run '^TestOwnedPolicy.*E2E$' ./pkg/provider/network

docker run "${common[@]}" -v /var/run/docker.sock:/var/run/docker.sock "${image}" \
	go test -count=1 -tags e2e -v -run '^TestDockerOwnedPolicyLifecycleE2E$' ./pkg/provider/docker

docker run "${common[@]}" \
	-v /usr/sbin/ip:/usr/sbin/ip:ro \
	-v /lib/x86_64-linux-gnu:/lib/x86_64-linux-gnu:ro \
	-v /lib64:/lib64:ro \
	-e PATH=/usr/sbin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/bin:/sbin:/bin \
	"${image}" go test -count=1 -tags e2e -v -run '^TestCheckpoint.*E2E$' ./pkg/api
