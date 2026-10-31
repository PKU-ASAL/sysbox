#!/usr/bin/env bash
# Sourced by on-host provider probes; the isolated API must have one executor.
set -euo pipefail
: "${SYSBOX_E2E_BINARY:?verified sysbox executable required}"
: "${SYSBOX_API_URL:?isolated API URL required}"
: "${SYSBOX_API_TOKEN:?API token required}"
: "${SYSBOX_E2E_AGENT_ID:?isolated executor ID required}"
test -x "$SYSBOX_E2E_BINARY"
identity="${SYSBOX_E2E_IDENTITY:-/etc/sysbox-host/identity.json}"
marker="${SYSBOX_E2E_LAB_MARKER:-/etc/sysbox-host/acceptance-lab.json}"
jq -e --arg id "$SYSBOX_E2E_AGENT_ID" '.id == $id' "$identity" >/dev/null
jq -e --arg id "$SYSBOX_E2E_AGENT_ID" '.agent_id == $id and .disposable == true and (.lab_id | test("^sysbox-p9-[a-f0-9]{12}$"))' "$marker" >/dev/null
curl --fail --silent --show-error --max-time 15 \
  -H "Authorization: Bearer ${SYSBOX_API_TOKEN}" "${SYSBOX_API_URL%/}/v1/agents" |
  jq -e --arg id "$SYSBOX_E2E_AGENT_ID" \
    '[.agents[] | select(.status == "online")] | length == 1 and .[0].id == $id' >/dev/null
