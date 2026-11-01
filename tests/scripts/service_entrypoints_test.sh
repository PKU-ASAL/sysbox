#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
for target in cli-plan cli-apply cli-destroy; do
 output=$(make -n "$target" BINARY=/usr/bin/true TOPO=docker-service)
 if [[ "$output" == *'--state '* || "$output" != *'--topology docker-service'* ]]; then
  echo "$target does not use the service topology" >&2; exit 1
 fi
done
for target in .agent-register api-deploy; do
 output=$(make -n "$target" BINARY=/usr/bin/true)
 if [[ "$output" == *'go build'* || "$output" == *'docker build'* ]]; then
  echo "$target rebuilds supplied artifacts" >&2; exit 1
 fi
done
output=$(make -n ci BINARY=/usr/bin/true)
[[ "$output" != *'--state /tmp/sysbox-ci-'* ]] || { echo 'CI still invokes local lifecycle planning' >&2; exit 1; }
echo 'Service entrypoint tests passed'
for script in tests/e2e/docker_launch_override.sh tests/e2e/docker_network_alias.sh; do
 if rg -- '--state|go build' "$script"; then
  echo "$script retains the removed local lifecycle path" >&2; exit 1
 fi
 rg -q 'service_host_check.sh' "$script"
done
