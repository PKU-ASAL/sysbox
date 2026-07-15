#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"
go run ./scripts/release/workflowcheck ci .forgejo/workflows/ci.yml
go run ./scripts/release/workflowcheck acceptance .forgejo/workflows/acceptance.yml
go run ./scripts/release/workflowcheck release .forgejo/workflows/release.yml
echo "Forgejo workflow contracts passed."
