#!/usr/bin/env bash
# Use the isolated post-effect persistence/stop-receipt gate, not a removed
# local CLI entry which fails before exercising storage at all.
set -euo pipefail
exec python3 "$(dirname "${BASH_SOURCE[0]}")/enospc.py" "$@"
