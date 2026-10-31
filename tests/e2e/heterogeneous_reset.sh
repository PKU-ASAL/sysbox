#!/usr/bin/env bash
set -euo pipefail
export SYSBOX_RESET_CYCLES=3
export SYSBOX_TARGETED_RESET=1
exec bash "$(dirname "${BASH_SOURCE[0]}")/heterogeneous_matrix.sh"
