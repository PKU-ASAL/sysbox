#!/usr/bin/env bash
# Run a test/acceptance command with fresh embedded helpers, including child Go
# processes. Do not exec: the trap must retain the overlay through completion.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repo_root}/scripts/release/helper-overlay.sh"
(($#)) || { echo 'usage: with-helper-overlay.sh COMMAND [ARG...]' >&2; exit 2; }
cd "${repo_root}"
case "${GOFLAGS:-}" in *-overlay*) echo 'refusing to replace an existing Go overlay' >&2; exit 2 ;; esac
tmp="$(mktemp -d /tmp/sysbox-helper-overlay.XXXXXX)"
trap 'rm -rf "${tmp}"' EXIT
prepare_helper_overlay "${repo_root}" "${tmp}" '-s -w'
export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-overlay=${tmp}/overlay.json"
"$@"
