#!/usr/bin/env python3
"""Run the durable-policy matrix only inside a named retained acceptance VM.

Build the current helper and provider tests with an embed overlay. The kernel
must include nftables; the supplied nft archive contains its userspace and
loader dependencies. Inputs are hashed in the retained report.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import uuid

import two_vms as lab

TEST = 'TestFirecrackerDurablePolicyFaultMatrixE2E'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--output', type=Path, required=True)
    for name in ('kernel', 'rootfs', 'firecracker', 'nft-archive'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    root, manifest = lab.load(args.root)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    source = Path(__file__).resolve().parents[2]
    output = args.output.resolve()
    helper = output / 'sysbox-init'
    binary = output / 'firecracker.test'
    overlay = output / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(source / 'pkg/provider/firecracker/initbin/sysbox-init.linux-amd64.bin'): str(helper)}}))
    env = dict(os.environ, CGO_ENABLED='0', GOCACHE=os.environ.get('GOCACHE', '/tmp/sysbox-gocache'))
    subprocess.run(['go', 'build', '-buildvcs=false', '-o', str(helper), './cmd/sysbox-init'], cwd=source, env=env, check=True)
    subprocess.run(['go', 'test', '-c', '-tags=e2e', '-overlay=' + str(overlay), '-o', str(binary), './pkg/provider/firecracker'], cwd=source, env=env, check=True)
    remote = '/var/tmp/sysbox-policy-' + uuid.uuid4().hex[:12]
    files = {'firecracker.test': binary, 'kernel': args.kernel, 'rootfs': args.rootfs,
             'firecracker': args.firecracker, 'nft.tar': args.nft_archive}
    report = {'version': 1, 'lab_id': manifest['id'], 'remote': remote, 'test': TEST,
              'scope': 'real nested KVM/vsock; whole microVM reboot and cold host provider reconstruction',
              'status': 'failed', 'artifacts': {name: {'path': str(path.resolve(strict=True)), 'sha256': lab.file_sha256(path)} for name, path in files.items()},
              'embedded_init_sha256': lab.file_sha256(helper)}
    lab.save(output / 'run.json', report)
    quote = shlex.quote
    result = lab.ssh(root, manifest, 'execution', 'mkdir -m 700 ' + quote(remote))
    if result.returncode:
        raise RuntimeError(result.stderr)
    for name, path in files.items():
        lab.upload(root, manifest, 'execution', path, remote + '/' + name)
    command = 'sudo chown -R root:root ' + quote(remote) + ' && sudo chmod 700 ' + quote(remote + '/firecracker.test') + ' ' + quote(remote + '/firecracker')
    command += ' && sudo env ' + ' '.join(k + '=' + quote(remote + '/' + v) for k, v in {
        'SYSBOX_POLICY_NFT_ARCHIVE': 'nft.tar', 'SYSBOX_FIRECRACKER_BIN': 'firecracker',
        'SYSBOX_FIRECRACKER_KERNEL': 'kernel', 'SYSBOX_FIRECRACKER_ROOTFS': 'rootfs'}.items())
    command += ' ' + quote(remote + '/firecracker.test') + ' -test.v -test.run=^' + TEST + '$ -test.timeout=4m'
    result = lab.ssh(root, manifest, 'execution', command, timeout=280)
    (output / 'stdout.log').write_text(result.stdout)
    (output / 'stderr.log').write_text(result.stderr)
    report['exit_code'] = result.returncode
    if result.returncode == 0 and '--- PASS: ' + TEST in result.stdout:
        report['status'] = 'passed'
    lab.save(output / 'run.json', report)
    raise SystemExit(result.returncode or (report['status'] != 'passed'))


if __name__ == '__main__':
    main()
