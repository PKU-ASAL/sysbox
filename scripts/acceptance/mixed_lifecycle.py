#!/usr/bin/env python3
"""Run current CLI mixed Docker NAT lifecycle only in an enrolled disposable lab."""
import argparse
import concurrent.futures
import ipaddress
import json
import os
from pathlib import Path
import shlex
import subprocess
import time
import uuid

import campaign
import host_fencing as h
import two_vms as lab

PROVIDERS = ('docker', 'firecracker', 'libvirt')


def verify_states(before, after):
    if not before.get('topology_uid') or before['topology_uid'] != after.get('topology_uid'):
        raise ValueError('topology identity changed')
    def index(state):
        rows = state['resources']
        found = {item['address']: item for item in rows}
        if len(found) != len(rows):
            raise ValueError('duplicate resource')
        return found
    old, new = index(before), index(after)
    if set(old) != set(new):
        raise ValueError('resource inventory changed')
    for name in PROVIDERS:
        key = 'sysbox_node.' + name
        a, b = old[key], new[key]
        if (a['driver'] != name or b['driver'] != name or
                any(a[k] != b[k] for k in ('owner_uid', 'resource_uid')) or
                b['generation'] <= a['generation'] or b['external_id'] == a['external_id']):
            raise ValueError('node reset identity conflict')
        def links(item):
            return [(link['name'], link.get('mac'), link.get('ip_prefixes')) for link in item['attachments']]
        if links(a) != links(b):
            raise ValueError('logical network coordinates changed')
    for key, a in old.items():
        if a['resource_type'] == 'sysbox_network':
            if any(a[k] != new[key][k] for k in ('owner_uid', 'resource_uid', 'generation', 'external_id')):
                raise ValueError('network identity changed')


def verify_translated_sources(observations, expected):
    if set(observations) != set(PROVIDERS):
        raise ValueError('all three guest probes required')
    for sources in observations.values():
        if len(sources) < 2 or any(source != expected for source in sources):
            raise ValueError('cross-host NAT source not proven')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cidr', default='10.59.0.0/24')
    for name in ('firecracker', 'kernel', 'rootfs', 'cloud'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    subnet = ipaddress.ip_network(args.cidr)
    if subnet.version != 4 or subnet.prefixlen != 24 or not subnet.is_private:
        raise ValueError('private IPv4 /24 required')
    root, manifest = lab.load(args.root)
    h.check_scope(root, manifest)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    source = Path(__file__).resolve().parents[2]
    source_hash = campaign.source_identity(source)[1]
    topology = 'p9-mixed-' + uuid.uuid4().hex[:10]
    remote = '/var/tmp/' + topology
    quote = shlex.quote
    report = dict(version=1, lab_id=manifest['id'], topology=topology,
                  source_sha256=source_hash, phase='building', status='failed', artifacts={})
    def save():
        lab.save(args.output / 'run.json', report)
    def execute(role, command, timeout=60):
        result = lab.ssh(root, manifest, role, command, timeout=timeout)
        number = len(list(args.output.glob('command-*.json')))
        (args.output / ('command-%03d.json' % number)).write_text(json.dumps(
            dict(role=role, command=command, exit_code=result.returncode,
                 stdout=result.stdout, stderr=result.stderr), indent=2))
        if result.returncode:
            raise RuntimeError(role + ': ' + result.stderr[-1800:] + result.stdout[-1800:])
        return result.stdout.strip()
    build_env = dict(os.environ, CGO_ENABLED='0')
    helper, binary, probe = [args.output.resolve() / name for name in ('helper', 'sysbox', 'probe')]
    subprocess.run(['go', 'build', '-buildvcs=false', '-o', str(helper), './cmd/sysbox-init'],
                   cwd=source, env=build_env, check=True)
    overlay = args.output.resolve() / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {
        str(source / 'pkg/provider/firecracker/initbin/sysbox-init.linux-amd64.bin'): str(helper)}}))
    for target, package, extra in ((binary, './cmd/sysbox', ['-overlay=' + str(overlay)]),
                                   (probe, './tests/e2e/matrixprobe', [])):
        subprocess.run(['go', 'build', '-buildvcs=false', *extra, '-o', str(target), package],
                       cwd=source, env=build_env, check=True)
    save()
    try:
        h.wait_boot(root, manifest, 'execution')
        # Refuse missing tools before creating any provider resource.
        execute('execution', 'command -v virsh && command -v genisoimage && command -v qemu-img && command -v ssh-keygen && command -v docker')
        execute('execution', 'mkdir -m 755 ' + quote(remote))
        assets = dict(sysbox=binary, probe=probe, helper=helper,
                      firecracker=args.firecracker, kernel=args.kernel, rootfs=args.rootfs, cloud=args.cloud)
        for name, path in assets.items():
            path = path.resolve(strict=True)
            digest = lab.file_sha256(path)
            lab.upload(root, manifest, 'execution', path, remote + '/' + name)
            actual = execute('execution', 'sha256sum ' + quote(remote + '/' + name)).split()[0]
            if actual != digest:
                raise RuntimeError('remote artifact mismatch: ' + name)
            report['artifacts'][name] = dict(sha256=digest, source=str(path))
        execute('execution', 'chmod 755 ' + quote(remote + '/sysbox') + ' ' + quote(remote + '/probe') +
                ' ' + quote(remote + '/firecracker') + '; chmod 644 ' + quote(remote + '/cloud') +
                '; sudo mkdir -m 700 ' + quote(remote + '/fc') +
                '; sudo mkdir -m 755 ' + quote(remote + '/lv') +
                '; ssh-keygen -q -t ed25519 -N "" -f ' + quote(remote + '/key'))
        public = execute('execution', 'cat ' + quote(remote + '/key.pub'))
        service = {'providers': {'firecracker': {'binary': remote + '/firecracker',
                    'kernel': remote + '/kernel', 'workdir': remote + '/fc'},
                    'libvirt': {'workdir': remote + '/lv'}}}
        local = args.output / 'service.json'
        local.write_text(json.dumps(service))
        lab.upload(root, manifest, 'execution', local, remote + '/service.json')
        text = (source / 'examples/heterogeneous-matrix/field.sysbox.hcl').read_text()
        prefix_ip = str(subnet.network_address).rsplit('.', 1)[0] + '.'
        text = text.replace('alpine:latest', 'alpine:3.22').replace(
            'cidr = "10.44.0.0/24"', 'cidr = "10.44.0.0/24"\n  nat = true')
        text = text.replace('memory    = "2048"', 'memory    = "512"').replace(
            'memory            = "1024"', 'memory            = "512"')
        for last in ('20', '30'):
            text = text.replace('ip      = "10.44.0.' + last + '/24"',
                                'ip      = "10.44.0.' + last + '/24"\n    gw = "10.44.0.1"')
        text = text.replace('10.44.0.', prefix_ip)
        local = args.output / 'field.sysbox.hcl'
        local.write_text(text)
        lab.upload(root, manifest, 'execution', local, remote + '/field.sysbox.hcl')
        env = dict(SYSBOX_CONFIG=remote + '/service.json', SYSBOX_KERNEL=remote + '/kernel',
                   SYSBOX_ROOTFS=remote + '/rootfs', SYSBOX_QCOW2=remote + '/cloud',
                   SYSBOX_MATRIX_SSH_PRIVATE_KEY=remote + '/key', SYSBOX_MATRIX_SSH_PUBLIC_KEY=public)
        cli = 'sudo env ' + ' '.join(k + '=' + quote(v) for k, v in env.items()) + ' ' + quote(remote + '/sysbox')
        state_path = remote + '/' + topology + '/state.json'
        cli += ' --state ' + quote(state_path) + ' -f ' + quote(remote + '/field.sysbox.hcl')
        probe_cmd = ('sudo env SYSBOX_CONFIG=' + quote(env['SYSBOX_CONFIG']) + ' ' + quote(remote + '/probe') +
                     ' -state ' + quote(state_path) + ' -topology ' + topology)
        ips = dict(zip(PROVIDERS, [prefix_ip + n for n in ('10', '20', '30')]))
        def state():
            return json.loads(execute('execution', 'sudo cat ' + quote(state_path + '.lifecycle/' + topology + '.json')))['state']
        def node_command(name, arguments, generation):
            if name == 'docker':
                return 'sudo docker exec sysbox-lab-' + topology + '-node-docker ' + arguments
            if name == 'firecracker':
                if arguments.startswith('ping '):
                    return probe_cmd + ' -target ' + arguments.split()[-1] + (' -pattern ' + arguments.split()[arguments.split().index('-p')+1] if '-p' in arguments.split() else '')
                operation = '-touch ' if arguments.startswith('touch ') else '-check-absent '
                return probe_cmd + ' ' + operation + quote(arguments.split()[-1])
            return ('ssh -i ' + quote(remote + '/key') +
                    ' -o BatchMode=yes -o ConnectTimeout=2 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=' +
                    quote(remote + '/known-' + generation) + ' sysbox@' + ips[name] + ' ' + quote(arguments))
        def wait_ssh(generation):
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                result = lab.ssh(root, manifest, 'execution', node_command('libvirt', 'true', generation), timeout=6)
                if result.returncode == 0:
                    return
                time.sleep(1)
            raise RuntimeError('new guest SSH unavailable')
        used_patterns=set()
        def connectivity(generation):
            wait_ssh(generation)
            for sender in PROVIDERS:
                for receiver in PROVIDERS:
                    if sender != receiver:
                        execute('execution', node_command(sender, 'ping -c 2 -W 2 ' + ips[receiver], generation), 30)
            observations = {}
            # One capture per source: no aggregate packet count can hide an untested guest.
            for name in PROVIDERS:
                pattern=uuid.uuid4().hex[:2]
                while pattern in used_patterns:
                    pattern=uuid.uuid4().hex[:2]
                used_patterns.add(pattern)
                ready = '/tmp/' + topology + '-' + generation + '-' + name + '.ready'
                program = ("import socket,time,json,pathlib;"
                           "s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_ICMP);s.settimeout(.5);"
                           "pattern=bytes.fromhex(" + repr(pattern) + ")*16;"
                           "pathlib.Path(" + repr(ready) + ").write_text('ready');end=time.monotonic()+8;rows=[]\n"
                           "while time.monotonic()<end:\n"
                           " try:\n"
                           "  data,addr=s.recvfrom(65535)\n"
                           "  offset=(data[0]&15)*4\n"
                           "  if data[offset]==8 and socket.inet_ntoa(data[16:20])=='10.77.0.10' and pattern in data[offset+8:]:rows.append(addr[0])\n"
                           " except socket.timeout:pass\n"
                           "print(json.dumps(rows))")
                with concurrent.futures.ThreadPoolExecutor() as pool:
                    future = pool.submit(lab.ssh, root, manifest, 'control',
                                         'sudo python3 -c ' + quote(program), timeout=15)
                    for _ in range(30):
                        check = lab.ssh(root, manifest, 'control', 'test -f ' + quote(ready), timeout=5)
                        if check.returncode == 0:
                            break
                        time.sleep(.1)
                    else:
                        raise RuntimeError('NAT observer not ready')
                    execute('execution', node_command(name, 'ping -p '+pattern+' -c 2 -W 2 10.77.0.10', generation), 30)
                    result = future.result()
                if result.returncode:
                    raise RuntimeError('NAT capture failed: ' + result.stderr)
                observations[name] = json.loads(result.stdout)
            verify_translated_sources(observations, '10.77.0.20')
            report[generation + '_nat_sources'] = observations
            save()
        report['phase'] = 'apply'
        save()
        execute('execution', cli + ' apply --auto-approve', 480)
        before = state()
        report['before_reset'] = before
        connectivity('before')
        for name in PROVIDERS:
            execute('execution', node_command(name, 'touch /tmp/sysbox-matrix-marker', 'before'))
        report['phase'] = 'reset'
        save()
        execute('execution', cli + ' reset --auto-approve', 480)
        after = state()
        report['after_reset'] = after
        verify_states(before, after)
        connectivity('after')
        for name in PROVIDERS:
            execute('execution', node_command(name, 'test ! -e /tmp/sysbox-matrix-marker', 'after'))
        report['phase'] = 'destroy'
        save()
        execute('execution', cli + ' destroy --auto-approve', 300)
        if state()['resources']:
            raise RuntimeError('state not empty')
        owner = after['topology_uid']
        for query in ('sudo docker ps -aq --filter label=sysbox.owner_uid=',
                      'sudo docker network ls -q --filter label=sysbox.owner_uid='):
            if execute('execution', query + quote(owner)):
                raise RuntimeError('owned Docker residue')
        # Preserve and independently check both reset generations using the
        # provider's read-only residue proof (including FC external sockets).
        for generation, snapshot in (('before',before),('after',after)):
            for item in snapshot['resources']:
                if item['resource_type'] != 'sysbox_node':
                    continue
                path=args.output / (generation+'-'+item['driver']+'-resource.json')
                path.write_text(json.dumps(item))
                remote_resource=remote+'/'+path.name
                lab.upload(root,manifest,'execution',path,remote_resource)
                execute('execution','sudo env SYSBOX_CONFIG='+quote(env['SYSBOX_CONFIG'])+' '+
                        quote(remote+'/probe')+' -absent-resource '+quote(remote_resource))
        if campaign.source_identity(source)[1] != source_hash:
            raise RuntimeError('source changed during acceptance')
        report.update(status='passed', phase='complete', cleanup='exact owner absent; state/journal/artifact evidence retained')
    except subprocess.TimeoutExpired as error:
        for name, raw in (('timeout-stdout.log', error.stdout), ('timeout-stderr.log', error.stderr)):
            (args.output / name).write_bytes(raw.encode() if isinstance(raw, str) else raw or b'')
        report['error']='remote timeout; original process and provider state must be inspected before retry'
        raise
    except Exception as error:
        report['error'] = str(error)
        raise
    finally:
        save()
    print(json.dumps({'status': report['status'], 'evidence': str(args.output / 'run.json')}))


if __name__ == '__main__':
    main()
