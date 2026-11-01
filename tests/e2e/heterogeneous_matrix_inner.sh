#!/usr/bin/env bash
# The former local-state execution entry now delegates to the sole API runner.
set -euo pipefail
exec bash "$(dirname "${BASH_SOURCE[0]}")/heterogeneous_matrix.sh" "$@"
