#!/usr/bin/env python3
"""Disposable-VM fixtures for lost deletion results; never reads historical labs."""
import argparse
import json
import uuid
from pathlib import Path
import host_fencing as h


def fixture(root, provider):
    root, manifest = h.two_vms.load(root)
    h.check_scope(root, manifest)
    name = 'delete-closure-' + uuid.uuid4().hex
    if provider == 'docker':
        external = h.guest(root, manifest, 'execution',
                           'sudo docker create --network none --name ' + name +
                           ' alpine:3.22 sleep 60')
        h.guest(root, manifest, 'execution', 'sudo docker rm ' + external)
        result = dict(driver=provider, external_id=external)
    else:
        external = str(uuid.uuid4())
        directory = '/var/tmp/sysbox-delete-closure/' + name
        definition = root / (name + '.xml')
        definition.write_text('<domain type="kvm"><name>' + name + '</name><uuid>' +
                              external + '</uuid><memory unit="MiB">32</memory><vcpu>1</vcpu>'
                              '<os><type arch="x86_64">hvm</type></os>'
                              '<devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices></domain>')
        h.two_vms.upload(root, manifest, 'execution', definition, '/var/tmp/' + name + '.xml')
        h.guest(root, manifest, 'execution', 'sudo mkdir -p ' + directory +
                '; sudo virsh define /var/tmp/' + name + '.xml >/dev/null')
        h.guest(root, manifest, 'execution', 'sudo virsh undefine ' + external +
                ' >/dev/null && sudo rmdir ' + directory)
        result = dict(driver=provider, external_id=external,
                      provider_state=dict(domain_name=name, domain_uuid=external,
                                          vm_dir=directory, disk_path=directory + '/disk.qcow2'))
    h.two_vms.save(root / (name + '.fixture.json'), result)
    print(json.dumps(result))


def observe(root, source, output, binary):
    root, manifest = h.two_vms.load(root)
    h.check_scope(root, manifest)
    remote = '/var/tmp/delete-observer-' + uuid.uuid4().hex
    h.two_vms.upload(root, manifest, 'execution', Path(binary), remote)
    h.two_vms.upload(root, manifest, 'execution', Path(source), remote + '.json')
    h.guest(root, manifest, 'execution', 'chmod 700 ' + remote)
    result = h.two_vms.ssh(root, manifest, 'execution',
            'sudo env LIBVIRT_DEFAULT_URI=qemu:///system SYSBOX_FENCE_OBSERVER_INPUT=' +
            remote + '.json SYSBOX_FENCE_OBSERVER_OUTPUT=' + remote + '.result ' + remote +
            ' -test.run=^TestFenceObservationAcceptance$ -test.v', timeout=120)
    if result.returncode:
        raise RuntimeError('observer failed: ' + result.stdout[-4000:] + result.stderr[-1000:])
    raw = h.guest(root, manifest, 'execution', 'sudo cat ' + remote + '.result')
    h.two_vms.save(Path(output), json.loads(raw))
    h.guest(root, manifest, 'execution', 'sudo rm -- ' + remote + ' ' + remote + '.json ' + remote + '.result')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('operation', choices=['fixture', 'observe'])
    p.add_argument('--root', required=True)
    p.add_argument('--provider', choices=['docker', 'libvirt'])
    p.add_argument('--input')
    p.add_argument('--output')
    p.add_argument('--binary')
    a = p.parse_args()
    if a.operation == 'fixture':
        if not a.provider: p.error('--provider required')
        fixture(a.root, a.provider)
    else:
        if not all([a.input, a.output, a.binary]): p.error('--input, --output, --binary required')
        observe(a.root, a.input, a.output, a.binary)
