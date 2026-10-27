#!/usr/bin/env bash
# Produce the portable Windows helper bundle without per-node credentials.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
output="${1:?new output directory required}"
mkdir -m 0700 "${output}"
output="$(cd "${output}" && pwd)"
cd "${repo_root}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags='-s -w' -o "${output}/sysbox-windows-task.exe" ./cmd/sysbox-windows-task
install -m 0644 scripts/windows/task/install.ps1 "${output}/install.ps1"
git rev-parse HEAD > "${output}/source-commit.txt"
if test -n "$(git status --porcelain --untracked-files=normal)"; then printf '%s\n' 'working tree differs from source commit' > "${output}/UNCOMMITTED"; fi
cd "${output}"
sha256sum sysbox-windows-task.exe install.ps1 source-commit.txt > SHA256SUMS
