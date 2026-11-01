#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${SYSBOX_LAB_ROOT:?verified disposable two-VM lab required}"
: "${SYSBOX_RELEASE_METADATA:?release metadata required}"
: "${SYSBOX_E2E_BINARY:?released binary required}"
: "${SYSBOX_E2E_HELPER:?released guest helper required}"
: "${SYSBOX_MIXED_REVISION:?published mixed-provider revision required}"
: "${SYSBOX_E2E_OUTPUT:?new evidence directory required}"
extra=()
if [[ "${SYSBOX_TARGETED_RESET:-0}" == 1 ]]; then extra+=(--targeted-reset); fi
exec python3 "$root/scripts/acceptance/mixed_lifecycle.py" \
 --root "$SYSBOX_LAB_ROOT" --release-metadata "$SYSBOX_RELEASE_METADATA" \
 --binary "$SYSBOX_E2E_BINARY" --helper "$SYSBOX_E2E_HELPER" \
 --revision "$SYSBOX_MIXED_REVISION" --output "$SYSBOX_E2E_OUTPUT" \
 --cidr "${SYSBOX_MIXED_CIDR:-10.59.0.0/24}" --reset-cycles "${SYSBOX_RESET_CYCLES:-1}" "${extra[@]}"
