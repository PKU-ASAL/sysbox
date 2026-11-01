#!/usr/bin/env bash
# Install an already enrolled identity and public keys. Does not start services.
set -euo pipefail
if [[ $EUID != 0 || $# != 3 ]]; then
    echo "Usage (root): $0 ENROLLED_IDENTITY ISSUER_PUBLIC_KEY HOST_PUBLIC_KEY" >&2
    exit 1
fi
worker_uid=${SYSBOX_AGENT_UID:-32000}
worker_gid=${SYSBOX_AGENT_GID:-32000}
worker_data=${SYSBOX_AGENT_DATA_DIR:-/var/lib/sysbox-agent}
worker_config=${SYSBOX_WORKER_CONFIG_DIR:-/etc/sysbox-worker}
host_config=${SYSBOX_HOST_CONFIG_DIR:-/etc/sysbox-host}
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# Reject old identities that retained the administrator credential.
python3 - "$1" "$2" "$3" <<'PY'
import json, pathlib, sys
identity = json.loads(pathlib.Path(sys.argv[1]).read_text())
if identity.get('token') or not all(identity.get(k) for k in ('id', 'api_url', 'secret')):
    raise SystemExit('Enroll a fresh identity without a retained API token')
for key in sys.argv[2:]:
    if len(pathlib.Path(key).read_bytes()) != 32:
        raise SystemExit('Expected a raw 32-byte Ed25519 public key')
PY
# Enrollment/rotation is explicit. Never replace an active service identity.
for dest in "$worker_data/identity.json" "$host_config/identity.json" "$worker_config/issuer.pub" "$worker_config/host.pub"; do
    if [[ -e $dest || -L $dest ]]; then
        echo "Refusing to replace existing identity/key: $dest" >&2
        exit 1
    fi
done
install -d -o root -g root -m 0755 "$worker_config"
install -d -o root -g root -m 0700 "$host_config"
install -d -o "$worker_uid" -g "$worker_gid" -m 0700 "$worker_data"
install -o root -g root -m 0600 "$1" "$host_config/identity.json"
install -o "$worker_uid" -g "$worker_gid" -m 0600 "$1" "$worker_data/identity.json"
install -o root -g root -m 0444 "$2" "$worker_config/issuer.pub"
install -o root -g root -m 0444 "$3" "$worker_config/host.pub"
install -o root -g root -m 0444 "$script_dir/docker/agent.yaml" "$worker_config/agent.yaml"
echo "Worker identity/public keys installed. Configure and start sysbox-host before sysbox-agent."
