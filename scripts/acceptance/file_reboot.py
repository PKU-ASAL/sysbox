#!/usr/bin/env python3
"""Real installer/SSH reboot fixture, restricted to an enrolled disposable VM."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
import sys
import uuid

import host_fencing as h


def main():
    p = argparse.ArgumentParser(__doc__)
    p.add_argument('operation', choices=('setup', 'call', 'ready', 'fence', 'cleanup'))
    p.add_argument('--root', required=True)
    p.add_argument('--session', required=True)
    p.add_argument('--probe')
    p.add_argument('--public-key')
    p.add_argument('--storage', choices=('tmpfs', 'persistent'), default='tmpfs')
    p.add_argument('--lost-outcome', action='store_true')
    a = p.parse_args()
    root, manifest = h.two_vms.load(a.root)
    h.check_scope(root, manifest)
    # The operator chooses a fresh session ID, never a remote path or command.
    uuid.UUID(a.session)
    target = '/var/tmp/sysbox-file-fence-' + a.session
    quote = shlex.quote
    if a.operation == 'setup':
        h.wait_boot(root, manifest, 'execution')
        if not a.probe or not a.public_key:
            raise ValueError('probe and public key required')
        h.guest(root, manifest, 'execution', 'sudo mkdir -m 700 ' + quote(target))
        remote_probe = '/var/tmp/file-fence-probe-' + a.session
        remote_key = '/var/tmp/file-fence-key-' + a.session
        h.two_vms.upload(root, manifest, 'execution', Path(a.probe), remote_probe)
        h.two_vms.upload(root, manifest, 'execution', Path(a.public_key), remote_key)
        h.guest(root, manifest, 'execution',
                'sudo mv ' + quote(remote_probe) + ' ' + quote(target + '/probe') +
                ' && sudo mv ' + quote(remote_key) + ' ' + quote(target + '/issuer.pub') +
                ' && sudo chmod 700 ' + quote(target + '/probe') +
                ' && sudo chown -R root:root ' + quote(target) +
                ' && sudo mkdir -m 700 ' + quote(target + '/target') +
                (' && sudo mount -t tmpfs -o size=16m,mode=0700 tmpfs ' + quote(target + '/target') if a.storage == 'tmpfs' else '') +
                (' && sudo touch ' + quote(target + '/lost-outcome') if a.lost_outcome else ''))
        print(json.dumps({'target': target + '/target/file'}))
    elif a.operation == 'call':
        args = ['ssh', '-F', '/dev/null', '-i', str(root / 'client'), '-p', str(manifest['ports']['execution']),
                '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'ConnectTimeout=5',
                '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(root / 'known_hosts'),
                'p9@127.0.0.1', 'sudo env SYSBOX_FILE_FENCE_ROOT=' + quote(target) + ' ' +
                quote(target + '/probe') + ' -test.run=^TestFileFenceAcceptanceProbe$']
        result = subprocess.run(args, input=sys.stdin.buffer.read(), capture_output=True, timeout=240)
        sys.stdout.buffer.write(result.stdout)
        sys.stderr.buffer.write(result.stderr)
        sys.exit(result.returncode)
    elif a.operation == 'ready':
        print(h.guest(root, manifest, 'execution', 'sudo cat ' + quote(target + '/ready')))
    elif a.operation == 'fence':
        # The shared controller verifies pidfd exit, same disk, fresh guest boot
        # and unchanged control VM. It retains the evidence in the enrolled lab.
        h.fence(root)
    elif a.operation == 'cleanup':
        h.guest(root, manifest, 'execution',
                'if sudo mountpoint -q ' + quote(target + '/target') + '; then sudo umount ' +
                quote(target + '/target') + ' || exit; fi; sudo rm -rf -- ' + quote(target))


if __name__ == '__main__':
    main()
