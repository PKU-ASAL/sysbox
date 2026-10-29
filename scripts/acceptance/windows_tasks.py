#!/usr/bin/env python3
"""Build and run native Windows/QGA durable-task acceptance in an isolated lab."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import uuid
import time
import tarfile
import hashlib
import two_vms as lab


def release_helper(metadata_path, output):
    metadata_path=Path(metadata_path).resolve()
    metadata=json.loads(metadata_path.read_text())
    target=[t for t in metadata['targets'] if t['os']=='windows' and t['architecture']=='amd64']
    if len(target)!=1:
        raise ValueError('exactly one released Windows helper required')
    target=target[0]
    if Path(target['archive']).name != target['archive']:
        raise ValueError('invalid release archive path')
    archive=metadata_path.parent/target['archive']
    if lab.file_sha256(archive)!=target['sha256']:
        raise ValueError('release archive digest mismatch')
    with tarfile.open(archive) as tar:
        members=tar.getmembers()
        if {m.name for m in members}!={'sysbox-windows-task.exe','install.ps1','build-metadata.json'} or len(members)!=3 or not all(m.isfile() for m in members):
            raise ValueError('unexpected Windows release members')
        raw={m.name:tar.extractfile(m).read() for m in members}
    info=json.loads(raw['build-metadata.json'])
    if info['commit']!=metadata['commit'] or info['version']!=metadata['tag']:
        raise ValueError('helper source identity mismatch')
    for name,key in [('sysbox-windows-task.exe','helper_sha256'),('install.ps1','installer_sha256')]:
        if hashlib.sha256(raw[name]).hexdigest()!=target[key]:
            raise ValueError('helper/installer digest mismatch')
        (output/name).write_bytes(raw[name])
    return metadata['commit']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--release-metadata', required=True, type=Path, help='verified release metadata; helper is never rebuilt by acceptance')
    parser.add_argument('--host-reboot', action='store_true', help='power-cycle only the pinned isolated lab after leaving an active task')
    parser.add_argument('--resume-root', help='retained matrix root inside isolated host, after verified guest reboot')
    parser.add_argument('--image', required=True, help='absolute qcow2 path inside execution VM')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    root, manifest = lab.load(args.root)
    if not args.image.startswith('/'):
        raise ValueError('absolute isolated-host image path required')
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    source = Path(__file__).resolve().parents[2]
    env = dict(os.environ, GOCACHE=os.environ.get('GOCACHE', '/tmp/sysbox-gocache'), CGO_ENABLED='0')
    windows = dict(env, GOOS='windows', GOARCH='amd64')
    files = {name: args.output.resolve() / name for name in ('sysbox-windows-task.exe', 'native.test.exe', 'libvirt.test')}
    if subprocess.check_output(['git','status','--porcelain','--untracked-files=all'],cwd=source,text=True).strip():
        raise ValueError('native test source must be a clean committed checkout')
    commit=release_helper(args.release_metadata,args.output.resolve())
    if subprocess.check_output(['git','rev-parse','HEAD'],cwd=source,text=True).strip()!=commit:
        raise ValueError('native test source must match the release commit')
    subprocess.run(['go', 'test', '-c', '-o', str(files['native.test.exe']), './pkg/windowstask'], cwd=source, env=windows, check=True)
    subprocess.run(['go', 'test', '-c', '-tags=e2e', '-o', str(files['libvirt.test']), './pkg/provider/libvirt'], cwd=source, env=env, check=True)
    files['install.ps1'] = args.output.resolve() / 'install.ps1'
    remote = '/var/tmp/sysbox-windows-tasks-' + uuid.uuid4().hex[:12]
    report = {'version': 1, 'lab_id': manifest['id'], 'image': args.image, 'remote': remote,
              'commit':commit,'release_metadata':str(args.release_metadata.resolve()), 'status': 'failed', 'resume_root': args.resume_root, 'artifacts': {n: {'sha256': lab.file_sha256(p)} for n, p in files.items()}}
    lab.save(args.output / 'run.json', report)
    quote = shlex.quote
    result = lab.ssh(root, manifest, 'execution', 'mkdir -m 700 ' + quote(remote))
    if result.returncode:
        raise RuntimeError(result.stderr)
    for name, path in files.items():
        lab.upload(root, manifest, 'execution', path, remote + '/' + name)
    command = 'sudo chown -R root:root ' + quote(remote) + ' && sudo chmod 700 ' + quote(remote + '/libvirt.test')
    values = {'SYSBOX_WINDOWS_TASK_IMAGE': args.image,
              'SYSBOX_WINDOWS_TASK_HELPER': remote + '/sysbox-windows-task.exe',
              'SYSBOX_WINDOWS_TASK_TEST': remote + '/native.test.exe',
              'SYSBOX_WINDOWS_TASK_INSTALLER': remote + '/install.ps1',
              'SYSBOX_WINDOWS_TASK_KEEP_FAILED': '1'}
    if args.host_reboot:
        values['SYSBOX_WINDOWS_HOST_CHECKPOINT'] = remote + '/host-checkpoint.json'
    command += ' && sudo env ' + ' '.join(k + '=' + quote(v) for k, v in values.items())
    test = 'TestWindowsRetainedGuestRebootCompletionE2E' if args.resume_root else 'TestWindowsQGADurableTaskFaultMatrixE2E'
    if args.resume_root:
        command += ' SYSBOX_WINDOWS_RETAINED_ROOT=' + quote(args.resume_root)
    command += ' ' + quote(remote + '/libvirt.test') + ' -test.run=^' + test + '$ -test.v -test.timeout=16m'
    result = lab.ssh(root, manifest, 'execution', command, timeout=1000)
    (args.output / 'stdout.log').write_text(result.stdout)
    (args.output / 'stderr.log').write_text(result.stderr)
    report['exit_code'] = result.returncode
    if result.returncode == 0 and ('--- PASS: ' + test) in result.stdout:
        report['status'] = 'passed'
        if args.host_reboot:
            if 'WINDOWS_HOST_FENCE_READY' not in result.stdout:
                raise RuntimeError('host fence checkpoint not ready')
            synced = lab.ssh(root, manifest, 'execution', 'sudo sync', timeout=30)
            if synced.returncode:
                raise RuntimeError('cannot flush host checkpoint')
            # stop verifies both QMP names against this private lab manifest;
            # retained start rejects still-live processes and reuses exact disks.
            lab.stop(root)
            time.sleep(2)
            lab.start(root)
            deadline = time.monotonic() + 180
            while True:
                ready = lab.ssh(root, manifest, 'execution', 'true', timeout=10)
                if ready.returncode == 0:
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError('restarted isolated host unavailable')
                time.sleep(3)
            recovery = 'sudo env SYSBOX_WINDOWS_HOST_CHECKPOINT=' + quote(values['SYSBOX_WINDOWS_HOST_CHECKPOINT'])
            recovery += ' ' + quote(remote + '/libvirt.test') + ' -test.run=^TestWindowsQGAWholeHostRestartRecoveryE2E$ -test.v -test.timeout=9m'
            recovered = lab.ssh(root, manifest, 'execution', recovery, timeout=560)
            (args.output / 'host-recovery.stdout.log').write_text(recovered.stdout)
            (args.output / 'host-recovery.stderr.log').write_text(recovered.stderr)
            report['host_recovery_exit_code'] = recovered.returncode
            if recovered.returncode or '--- PASS: TestWindowsQGAWholeHostRestartRecoveryE2E' not in recovered.stdout:
                report['status'] = 'failed'
                report['exit_code'] = recovered.returncode or 1
    lab.save(args.output / 'run.json', report)
    raise SystemExit(report['exit_code'] or (report['status'] != 'passed'))


if __name__ == '__main__':
    main()
