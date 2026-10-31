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
