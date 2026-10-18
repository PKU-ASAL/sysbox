#!/usr/bin/env python3
"""Explicit whole-VM fencing for newly created disposable acceptance labs.

This establishes kernel/process exclusion only. It cannot produce a successful
Run, delete receipt or recovery authorization for arbitrary historical effects.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import shlex
import socket
import struct
import subprocess
import time
import uuid

import two_vms


def validate_proof(value):
    required = ('lab_id', 'request_id', 'old_boot', 'new_boot')
    if (any(not value.get(k) for k in required) or value.get('role') != 'execution'
            or value['old_boot'] == value['new_boot']
            or value.get('qemu_exited') is not True
            or value.get('control_boot_unchanged') is not True
            or value.get('provider_scope') != 'local-only'
            or value.get('operation_result') != 'unknown'):
        raise ValueError('incomplete isolated-host fencing proof')


def check_scope(root, manifest):
    path = root / 'fencing-scope.json'
    if path.is_symlink():
        raise ValueError('symlinked fencing scope')
    scope = json.loads(path.read_text())
    if (scope.get('version') != 1 or scope.get('lab_id') != manifest['id']
            or scope.get('root') != str(root) or scope.get('role') != 'execution'
            or scope.get('disposable') is not True):
        raise ValueError('lab is not explicitly scoped for disposable execution fencing')
    return scope


def create(root, base):
    # Keep memory bounded on the shared developer host. Both guests have fresh
    # disks and SSH identities; retained/historical directories are refused.
    two_vms.ROLES = {'control': (1, 384, '10.77.0.10'),
                     'execution': (2, 1024, '10.77.0.20')}
    two_vms.up(root, base)
    root, manifest = two_vms.load(root)
    two_vms.save(root / 'fencing-scope.json', {
        'version': 1, 'lab_id': manifest['id'], 'root': str(root),
        'role': 'execution', 'disposable': True})


def guest(root, manifest, role, command, timeout=30):
    result = two_vms.ssh(root, manifest, role, command, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{role} command failed: {result.stderr[-1000:]}')
    return result.stdout.strip()


def wait_boot(root, manifest, role, old=None):
    deadline = time.monotonic() + 240
    while time.monotonic() < deadline:
        try:
            result = two_vms.ssh(root, manifest, role,
                                'cat /proc/sys/kernel/random/boot_id', timeout=10)
            boot = result.stdout.strip()
            uuid.UUID(boot)
            if result.returncode == 0 and (old is None or boot != old):
                return boot
        except (ValueError, RuntimeError, subprocess.TimeoutExpired):
            pass
        time.sleep(2)
    raise RuntimeError(f'{role} fresh boot not verified')


def capture_vm(root, manifest):
    directory = root / 'execution'
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('untrusted execution directory')
    for name in ('disk.qcow2', 'seed.iso'):
        source = directory / name
        if source.is_symlink() or not source.is_file():
            raise ValueError('untrusted execution storage')
    pidpath = directory / 'pid'
    monitor = directory / 'qmp'
    if pidpath.is_symlink() or monitor.is_symlink():
        raise ValueError('untrusted VM process locator')
    pid = int(pidpath.read_text())
    descriptor = os.pidfd_open(pid)
    try:
        args = Path(f'/proc/{pid}/cmdline').read_bytes().rstrip(b'\0').split(b'\0')
        args = [x.decode() for x in args]
        expected = manifest['id'] + '-execution'
        if (Path(f'/proc/{pid}/exe').resolve().name != 'qemu-system-x86_64'
                or '-name' not in args or args[args.index('-name') + 1] != expected
                or not any(f'file={directory}/disk.qcow2,' in x for x in args)
                or not any(f'unix:{monitor},' in x for x in args)):
            raise ValueError('QEMU immutable process/drive identity mismatch')
        with socket.socket(socket.AF_UNIX) as peer:
            peer.connect(str(monitor))
            peer_pid, _, _ = struct.unpack('3i', peer.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            if peer_pid != pid:
                raise ValueError('QMP peer does not belong to anchored QEMU')
        if two_vms.qmp(monitor, 'query-name')['name'] != expected:
            raise ValueError('QMP name mismatch')
        start = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()[19]
        return descriptor, pid, start, args
    except BaseException:
        os.close(descriptor)
        raise


def prepare(root):
    root, manifest = two_vms.load(root)
    check_scope(root, manifest)
    wait_boot(root, manifest, 'control')
    wait_boot(root, manifest, 'execution')
    print('installing packages in the new execution VM only', flush=True)
    guest(root, manifest, 'execution',
          'sudo cloud-init status --wait >/dev/null && sudo apt-get update -qq && '
          'sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq '
          'docker.io libvirt-daemon-system libvirt-clients qemu-system-x86 '
          '> /var/tmp/fencing-packages.log 2>&1', timeout=1800)
    image = root / 'alpine.tar'
    two_vms.run('docker', 'save', '-o', str(image), 'alpine:3.22')
    two_vms.upload(root, manifest, 'execution', image, '/var/tmp/fencing-alpine.tar')
    guest(root, manifest, 'execution', 'sudo docker load -i /var/tmp/fencing-alpine.tar >/dev/null; sudo modprobe kvm_intel')


def install_canaries(root, manifest, request_id):
    name = 'fence-' + request_id
    container = guest(root, manifest, 'execution',
                      'sudo docker run -d --network none --restart=no --name ' + name +
                      ' --label sysbox.fence=' + request_id + ' alpine:3.22 sleep 1800')
    two_vms.save(root / ('setup-' + request_id + '.json'), {'request_id': request_id, 'docker_id': container})
    # This real Docker exec waits for an in-flight process. Fencing must stop the
    # whole guest, not merely the client shell or a daemon process.
    guest(root, manifest, 'execution',
          'sudo sh -c ' + shlex.quote('docker exec ' + container +
          ' sleep 1800 >/var/tmp/' + name + '.exec.log 2>&1 &'))
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        processes = guest(root, manifest, 'execution', 'sudo docker top ' + container + ' -eo pid,args')
        if processes.count('sleep 1800') >= 2:
            break
        time.sleep(0.2)
    else:
        raise RuntimeError('Docker exec was not confirmed in flight')
    if guest(root, manifest, 'execution', 'sudo virsh uri') != 'qemu:///system':
        raise RuntimeError('remote libvirt endpoints are outside the fencing scope')
    domain_id = str(uuid.uuid4())
    domain = (f'<domain type="kvm"><name>{name}</name><uuid>{domain_id}</uuid>'
              '<memory unit="MiB">128</memory><vcpu>1</vcpu>'
              '<os><type arch="x86_64">hvm</type></os>'
              '<devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices></domain>')
    definition = root / (name + '.xml')
    definition.write_text(domain)
    two_vms.upload(root, manifest, 'execution', definition, '/var/tmp/' + name + '.xml')
    guest(root, manifest, 'execution', 'sudo virsh define /var/tmp/' + name + '.xml >/dev/null; sudo virsh start ' + domain_id + ' >/dev/null')
    two_vms.save(root / ('setup-' + request_id + '.json'), {'request_id': request_id, 'docker_id': container, 'domain_uuid': domain_id})
    observed = guest(root, manifest, 'execution',
                     "sudo docker inspect --format '{{.State.Running}}' " + container +
                     '; sudo virsh domstate ' + domain_id).splitlines()
    if observed != ['true', 'running']:
        raise RuntimeError('provider canaries are not running')
    guest(root, manifest, 'execution', 'sudo sync')
    return {'docker_id': container, 'domain_uuid': domain_id, 'docker_exec_inflight': True, 'libvirt_endpoint': 'qemu:///system', 'external_storage': False}


def fence(root):
    if not hasattr(os, 'pidfd_open'):
        raise RuntimeError('pidfd-capable Python required; use /usr/bin/python3 on this host')
    root, manifest = two_vms.load(root)
    check_scope(root, manifest)
    with (root / 'fencing.lock').open('a') as guard:
        fcntl.flock(guard, fcntl.LOCK_EX | fcntl.LOCK_NB)
        request_id = uuid.uuid4().hex
        path = root / ('fence-' + request_id + '.json')
        old_boot = wait_boot(root, manifest, 'execution')
        control_boot = wait_boot(root, manifest, 'control')
        provider = install_canaries(root, manifest, request_id)
        descriptor, pid, start, args = capture_vm(root, manifest)
        evidence = dict(lab_id=manifest['id'], role='execution', request_id=request_id,
                        old_boot=old_boot, control_boot=control_boot, provider_scope='local-only',
                        operation_result='unknown', provider=provider, old_qemu_pid=pid,
                        old_qemu_start=start, status='intent', qemu_exited=False)
        disk_before = (root / 'execution' / 'disk.qcow2').stat()
        evidence['disk_device_inode'] = [disk_before.st_dev, disk_before.st_ino]
        two_vms.save(path, evidence)
        try:
            print('powering off the exact isolated execution VM', flush=True)
            # QMP quit removes virtual power without guest OS shutdown. The
            # pidfd ties exit confirmation to this process, independent of PID reuse.
            try:
                two_vms.qmp(root / 'execution' / 'qmp', 'quit')
            except (ConnectionResetError, json.JSONDecodeError):
                pass  # Only pidfd readiness below can turn this into confirmed exit.
            poll = select.poll()
            poll.register(descriptor, select.POLLIN)
            if not poll.poll(15000):
                raise RuntimeError('original QEMU exit not confirmed; do not restart')
            evidence['qemu_exited'] = True
            evidence['status'] = 'power_off_confirmed'
            two_vms.save(path, evidence)
        finally:
            os.close(descriptor)
        directory = root / 'execution'
        for name in ('pid', 'qmp'):
            source = directory / name
            if source.exists():
                source.rename(directory / (request_id + '.' + name))
        # The same disk and pinned SSH host identity are reused. No configuration
        # supplied by a remote workload is interpreted as a host command.
        disk_after = (directory / 'disk.qcow2').stat()
        if [disk_after.st_dev, disk_after.st_ino] != evidence['disk_device_inode']:
            raise RuntimeError('execution disk identity changed while fenced')
        two_vms.run(*args)
        evidence['new_boot'] = wait_boot(root, manifest, 'execution', old_boot)
        evidence['control_boot_unchanged'] = wait_boot(root, manifest, 'control') == control_boot
        evidence['status'] = 'new_boot_verified'
        two_vms.save(path, evidence)
        state = guest(root, manifest, 'execution',
                      "sudo docker inspect --format '{{.State.Running}}' " + provider['docker_id'] +
                      '; sudo virsh domstate ' + provider['domain_uuid']).splitlines()
        if state != ['false', 'shut off']:
            raise RuntimeError('provider workload restarted unexpectedly: ' + repr(state))
        evidence['post_boot_provider_state'] = state
        evidence['status'] = 'fenced'
        validate_proof(evidence)
        two_vms.save(path, evidence)
        # Cleaning only the two random test identities, after independent stops.
        guest(root, manifest, 'execution',
              'sudo docker rm ' + provider['docker_id'] + ' >/dev/null; sudo virsh undefine ' +
              provider['domain_uuid'] + ' >/dev/null')
        evidence['test_resources_cleaned'] = True
        two_vms.save(path, evidence)
        print(json.dumps({'evidence': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
                          'status': 'fenced', 'operation_result': 'unknown'}), flush=True)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['create', 'prepare', 'fence'])
    parser.add_argument('--root', required=True)
    parser.add_argument('--base')
    args = parser.parse_args()
    if args.operation == 'create':
        if not args.base: parser.error('--base required')
        create(args.root, args.base)
    elif args.operation == 'prepare': prepare(args.root)
    else: fence(args.root)

if __name__ == '__main__': main()
