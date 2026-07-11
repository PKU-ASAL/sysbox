#!/usr/bin/env bash
# Capture a minimal mixed-topology episode:
#   mixed + opencode actor + Tetragon + deterministic curl prompt
#
# Usage:
#   sudo -E examples/mixed/lab.sh up
#   sudo -E examples/mixed-capture/capture_opencode_tetragon.sh
#
# Optional:
#   TETRAGON_CAPTURE_CMD='tetragon --export-filename {output}' \
#     sudo -E examples/mixed-capture/capture_opencode_tetragon.sh --episode ep-001

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
STATE_FILE="${REPO_ROOT}/.sysbox/runs/mixed/state.json"

if [ ! -f "${STATE_FILE}" ]; then
  echo "ERROR: mixed state not found: ${STATE_FILE}" >&2
  echo "Run first: sudo -E examples/mixed/lab.sh up" >&2
  exit 1
fi

cd "${REPO_ROOT}"

exec python3 examples/mixed-capture/capture_opencode_tetragon.py \
  --state "${STATE_FILE}" \
  "$@"
