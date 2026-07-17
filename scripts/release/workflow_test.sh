#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
go test -count=1 ./scripts/release/workflowcheck
go run ./scripts/release/workflowcheck ci .github/workflows/ci.yml
go run ./scripts/release/workflowcheck release .github/workflows/release.yml
echo "GitHub workflow contracts passed."
