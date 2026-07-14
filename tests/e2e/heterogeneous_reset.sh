#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SYSBOX_MATRIX_INNER=tests/e2e/heterogeneous_reset_inner.sh \
  bash "${root}/tests/e2e/heterogeneous_matrix.sh"
