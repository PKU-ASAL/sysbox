#!/usr/bin/env python3
"""Run exact unknown-file stop on nested KVM/vsock in an enrolled disposable VM.

The wrapper builds static test binaries and current sysbox-init itself. A Go
build overlay embeds that helper without modifying tracked release binaries.
No production or historical host is enrolled or modified by this script.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import uuid

import campaign
import host_fencing as h
import two_vms

TESTS = ['TestFirecrackerUnknownPersistentFileStopE2E', 'TestFirecrackerFenceRequiresExactTapAbsenceE2E']
PACKAGE = 'github.com/oslab/sysbox/pkg/provider/firecracker'


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', required=True)
    for name in ('firecracker', 'kernel', 'rootfs', 'output'):
        p.add_argument('--' + name, type=Path, required=True)
    a = p.parse_args()
    root, manifest = two_vms.load(a.root)
    h.check_scope(root, manifest)
    a.output.mkdir(mode=0o700, parents=False, exist_ok=False)
    source = Path(__file__).resolve().parents[2]
    source_hash = campaign.source_identity(source)[1]
    build_env = dict(os.environ, CGO_ENABLED='0')
    built = {name: (a.output / name).resolve() for name in ('init', 'file_probe', 'provider_test')}
    overlay = (a.output / 'overlay.json').resolve()
    overlay.write_text(json.dumps({'Replace': {str(source / 'pkg/provider/firecracker/initbin/sysbox-init.linux-amd64.bin'): str(built['init'])}}))
    commands = [
        ['go', 'build', '-buildvcs=false', '-o', str(built['init']), './cmd/sysbox-init'],
        ['go', 'test', '-c', '-o', str(built['file_probe']), './pkg/guesttask'],
        ['go', 'test', '-c', '-tags=e2e', '-overlay=' + str(overlay), '-o', str(built['provider_test']), './pkg/provider/firecracker'],
    ]
    for command in commands:
        subprocess.run(command, cwd=source, env=build_env, check=True)
    a.file_probe, a.provider_test = built['file_probe'], built['provider_test']
    session = str(uuid.uuid4())
    remote = '/var/tmp/sysbox-vsock-stop-' + session
    quote = shlex.quote
    artifacts = {}
    for name in ('provider_test', 'file_probe', 'firecracker', 'kernel', 'rootfs'):
        path = getattr(a, name).resolve(strict=True)
        artifacts[name] = {'path': str(path), 'sha256': two_vms.file_sha256(path)}
    report = {'version': 1, 'lab_id': manifest['id'], 'session': session,
              'source_sha256': source_hash, 'artifacts': artifacts,
              'scope': 'real nested KVM/vsock, same-boot local persistent-file stop',
              'tests': TESTS, 'iterations': 2, 'build_commands': commands,
              'embedded_init_sha256': two_vms.file_sha256(built['init']),
              'cleanup': 'not-attempted', 'status': 'failed'}
    succeeded = False
    try:
        h.wait_boot(root, manifest, 'execution')
        h.guest(root, manifest, 'execution', 'mkdir -m 700 ' + quote(remote))
        for name, artifact in artifacts.items():
            two_vms.upload(root, manifest, 'execution', Path(artifact['path']), remote + '/' + name)
        h.guest(root, manifest, 'execution',
                'sudo chown -R root:root ' + quote(remote) +
                ' && sudo chmod 700 ' + ' '.join(quote(remote + '/' + n) for n in ('provider_test', 'file_probe', 'firecracker')))
        env = {'SYSBOX_FILE_FENCE_PROBE': remote + '/file_probe',
               'SYSBOX_ATTACHMENT_E2E': '1',
               'SYSBOX_EXPECTED_INIT_SHA256': report['embedded_init_sha256'],
               'SYSBOX_FIRECRACKER_BIN': remote + '/firecracker',
               'SYSBOX_FIRECRACKER_KERNEL': remote + '/kernel',
               'SYSBOX_FIRECRACKER_ROOTFS': remote + '/rootfs'}
        command = 'sudo env ' + ' '.join(k + '=' + quote(v) for k, v in env.items())
        command += ' ' + quote(remote + '/provider_test') + ' -test.v -test.run=' + quote('^(' + '|'.join(TESTS) + ')$') + ' -test.count=2 -test.timeout=5m'
        result = two_vms.ssh(root, manifest, 'execution', command, timeout=320)
        (a.output / 'stdout.log').write_text(result.stdout)
        (a.output / 'stderr.log').write_text(result.stderr)
        events = subprocess.run(['go', 'tool', 'test2json', '-t', '-p', PACKAGE],
                                input=result.stdout, text=True, capture_output=True, check=True).stdout
        (a.output / 'events.jsonl').write_text(events)
        if result.returncode:
            raise RuntimeError('real vsock test failed; retained stdout/stderr')
        campaign.verify_events(events.encode(), TESTS, PACKAGE)
        counts = {name: {'run': 0, 'pass': 0} for name in TESTS}
        for line in events.splitlines():
            event = json.loads(line)
            if event.get('Test') in counts and event.get('Action') in ('run', 'pass'):
                counts[event['Test']][event['Action']] += 1
        if any(count != {'run': 2, 'pass': 2} for count in counts.values()):
            raise RuntimeError('exactly two successful test iterations required')
        report['verified_iterations'] = counts
        if source_hash != campaign.source_identity(source)[1]:
            raise RuntimeError('source changed during real acceptance')
        succeeded = True
    except subprocess.TimeoutExpired as error:
        for name, raw in (('stdout.log', error.stdout), ('stderr.log', error.stderr)):
            (a.output / name).write_bytes(raw.encode() if isinstance(raw, str) else raw or b'')
        report['error'] = 'SSH timeout; test process cleanup unconfirmed'
        raise
    finally:
        # A timed-out test may leave a microVM alive. Preserve its input path
        # and report for exact operator inspection instead of broad cleanup.
        if succeeded:
            try:
                h.guest(root, manifest, 'execution', 'sudo rm -rf -- ' + quote(remote))
                report['cleanup'] = 'passed'
                report['status'] = 'passed'
            except Exception as error:
                report['cleanup'] = 'failed'
                report['error'] = str(error)
                raise
            finally:
                (a.output / 'run.json').write_text(json.dumps(report, indent=2) + '\n')
        else:
            report['cleanup'] = 'retained-for-inspection'
            report['remote_directory'] = remote
            (a.output / 'run.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))


if __name__ == '__main__':
    main()
