#!/usr/bin/env python3
"""Run the released API mixed-provider lifecycle in an enrolled disposable lab."""
import argparse
import concurrent.futures
import ipaddress
import re
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


def verify_states(before, after, targets=PROVIDERS):
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
                (name in targets and (b['generation'] <= a['generation'] or b['external_id'] == a['external_id'])) or
                (name not in targets and (b['generation'] != a['generation'] or b['external_id'] != a['external_id']))):
            raise ValueError('node reset identity conflict')
        def links(item):
            return [(link['name'], link.get('mac'), link.get('ip_prefixes')) for link in item['attachments']]
        if links(a) != links(b):
            raise ValueError('logical network coordinates changed')
    for key, a in old.items():
        if a['resource_type'] in ('sysbox_image', 'sysbox_kernel'):
            if any(a.get(k) != new[key].get(k) for k in ('owner_uid', 'resource_uid', 'generation', 'external_id')):
                raise ValueError('baseline identity changed')
            if a.get('attributes', {}).get('sha256') != new[key].get('attributes', {}).get('sha256'):
                raise ValueError('baseline digest changed')
        if a['resource_type'] == 'sysbox_network':
            if any(a[k] != new[key][k] for k in ('owner_uid', 'resource_uid', 'generation', 'external_id')):
                raise ValueError('network identity changed')


def verify_translated_sources(observations, expected):
    if set(observations) != set(PROVIDERS):
        raise ValueError('all three guest probes required')
    for sources in observations.values():
        if len(sources) < 2 or any(source != expected for source in sources):
            raise ValueError('cross-host NAT source not proven')


def release_inputs(metadata_path, binary, helper):
    metadata = json.loads(Path(metadata_path).read_text())
    targets = [target for target in metadata['targets'] if target['os'] == 'linux' and target['architecture'] == 'amd64']
    if len(targets) != 1 or not metadata.get('commit'):
        raise ValueError('one amd64 release target and commit required')
    for path, key in ((binary, 'sysbox_sha256'), (helper, 'sysbox_init_sha256')):
        if lab.file_sha256(Path(path)) != targets[0][key]:
            raise ValueError('release executable checksum mismatch: ' + key)
    return Path(binary).resolve(strict=True), Path(helper).resolve(strict=True), metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--release-metadata', type=Path, required=True)
    parser.add_argument('--revision', required=True, help='published mixed-provider revision with pinned URLs and prepared host configuration')
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--helper', type=Path)
    parser.add_argument('--cidr', default='10.59.0.0/24')
    parser.add_argument('--reset-cycles', type=int, default=1)
    parser.add_argument('--targeted-reset', action='store_true')
    args = parser.parse_args()
    if not 1 <= args.reset_cycles <= 3:
        raise ValueError('reset cycles must be between one and three')
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", args.revision):
        raise ValueError("published revision digest required")
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
    if not args.binary or not args.helper:
        raise ValueError('release metadata requires binary and helper')
    binary, helper, metadata = release_inputs(args.release_metadata, args.binary, args.helper)
    if subprocess.check_output(['git','rev-parse','HEAD'],cwd=source,text=True).strip() != metadata['commit']:
        raise ValueError('probe source must match release commit')
    if subprocess.check_output(['git','status','--porcelain','--untracked-files=all'],cwd=source,text=True).strip():
        raise ValueError('probe source must be clean')
    report['release_commit'] = metadata['commit']
    report['revision'] = args.revision
    probe = args.output.resolve() / 'probe'
    subprocess.run(['go','build','-buildvcs=false','-o',str(probe),'./tests/e2e/matrixprobe'],cwd=source,env=dict(os.environ,CGO_ENABLED='0'),check=True)
    def api(method, path, payload=None, key=None):
        code = ("import sys,json;sys.path.insert(0,'/home/p9');from cross_host_run import request_json;"
                "c=json.load(open('/etc/sysbox-api/api1.json'));"
                "print(json.dumps(request_json('http://10.77.0.10:19870',c['api']['token'],"+
                repr(method)+","+repr(path)+","+repr(payload)+","+repr(key)+")))")
        return json.loads(execute('control','sudo python3 -c '+quote(code)))
    operation_counts = {}
    def lifecycle(operation, target=None):
        operation_counts[operation] = operation_counts.get(operation, 0) + 1
        body = {'revision':args.revision} if operation != 'destroy' else {}
        if target is not None: body['target'] = 'sysbox_node.' + target
        accepted = api('POST','/v1/topologies/'+topology+'/'+operation,body,topology+'-'+operation+'-'+str(operation_counts[operation]))
        report[operation+'_run']=accepted;save()
        deadline=time.monotonic()+600
        while time.monotonic()<deadline:
            result=api('GET','/v1/runs/'+accepted['run_id'])
            report['latest_run']=result;save()
            if result['status']=='done':return
            if result['status'] in ('failed','cancelled'):raise RuntimeError('lifecycle failed; retained evidence')
            time.sleep(2)
        raise RuntimeError('lifecycle unresolved; retained evidence')
    def state():
        return api('GET','/v1/topologies/'+topology+'/state')
    def unchanged_plan():
        plan = api('POST','/v1/topologies/'+topology+'/plan',{'revision':args.revision})
        if not plan.get('actions') or any(a['action'] != 'no-op' for a in plan['actions']):
            raise RuntimeError('expected unchanged plan')
        report.setdefault('plans', []).append(plan)
        save()
    try:
        h.wait_boot(root, manifest, 'execution')
        expected=lab.file_sha256(binary)
        for role in lab.ROLES:
            if execute(role,'sha256sum /usr/local/bin/sysbox').split()[0] != expected:
                raise RuntimeError('deployed binary differs from release: '+role)
        execute('execution','mkdir -m 755 '+quote(remote))
        lab.upload(root,manifest,'execution',probe,remote+'/probe')
        lab.upload(root,manifest,'control',source/'scripts/acceptance/cross_host_run.py','/home/p9/cross_host_run.py')
        guest_runner = source/'scripts/acceptance/matrix_guest.py'
        lab.upload(root,manifest,'control',guest_runner,'/home/p9/matrix_guest.py')
        prefix_ip=str(subnet.network_address).rsplit('.',1)[0]+'.'
        ips=dict(zip(PROVIDERS,[prefix_ip+n for n in ('10','20','30')]))
        env={'SYSBOX_CONFIG':'/etc/sysbox-host/sysbox.yaml'}
        def node_command(name, arguments, generation):
            return 'sudo python3 /home/p9/matrix_guest.py '+quote(topology)+' '+quote(name)+' '+quote(json.dumps(shlex.split(arguments)))
        def wait_ssh(generation):
            # Public signed guest execution is the readiness check for all providers.
            execute('control',node_command('libvirt','true',generation),180)
        used_patterns=set()
        def connectivity(generation):
            wait_ssh(generation)
            for sender in PROVIDERS:
                for receiver in PROVIDERS:
                    if sender != receiver:
                        execute('control', node_command(sender, 'ping -c 2 -W 2 ' + ips[receiver], generation), 30)
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
                    execute('control', node_command(name, 'ping -p '+pattern+' -c 2 -W 2 10.77.0.10', generation), 30)
                    result = future.result()
                if result.returncode:
                    raise RuntimeError('NAT capture failed: ' + result.stderr)
                observations[name] = json.loads(result.stdout)
            verify_translated_sources(observations, '10.77.0.20')
            report[generation + '_nat_sources'] = observations
            save()
        report['phase'] = 'apply'
        save()
        lifecycle('apply')
        before = state()
        unchanged_plan()
        original = before
        report['before_reset'] = before
        observed_providers = {r['driver'] for r in before['resources'] if r['resource_type'] == 'sysbox_node'}
        if observed_providers != set(PROVIDERS):
            raise RuntimeError('revision must contain all three mixed providers')
        connectivity('before')
        for name in PROVIDERS:
            execute('control', node_command(name, 'touch /tmp/sysbox-matrix-marker', 'before'))
        for cycle in range(args.reset_cycles):
            report['phase'] = 'reset'
            save()
            lifecycle('reset')
            after = state()
            report.setdefault('reset_states', []).append(after)
            report['after_reset'] = after
            verify_states(before, after)
            unchanged_plan()
            connectivity('after-'+str(cycle))
            for name in PROVIDERS:
                execute('control', node_command(name, 'test ! -e /tmp/sysbox-matrix-marker', 'after'))
                if cycle + 1 < args.reset_cycles:
                    execute('control', node_command(name, 'touch /tmp/sysbox-matrix-marker', 'after'))
            before = after
        if args.targeted_reset:
            for target in PROVIDERS:
                for name in PROVIDERS:
                    execute('control', node_command(name, 'touch /tmp/sysbox-target-marker', 'target'))
                lifecycle('reset', target)
                after = state()
                verify_states(before, after, (target,))
                unchanged_plan()
                report['reset_states'].append(after)
                for name in PROVIDERS:
                    probe_args = 'test ! -e /tmp/sysbox-target-marker' if name == target else 'test -e /tmp/sysbox-target-marker'
                    execute('control', node_command(name, probe_args, 'target'))
                connectivity('target-'+target)
                before = after
                save()
        report['phase'] = 'destroy'
        save()
        lifecycle('destroy')
        if state()['resources']:
            raise RuntimeError('state not empty')
        owner = after['topology_uid']
        for query in ('sudo docker ps -aq --filter label=sysbox.owner_uid=',
                      'sudo docker network ls -q --filter label=sysbox.owner_uid='):
            if execute('execution', query + quote(owner)):
                raise RuntimeError('owned Docker residue')
        # The network is retained across resets, so one recorded identity suffices.
        for resource in original['resources']:
            if resource['resource_type'] != 'sysbox_network':
                continue
            attrs = resource.get('attributes', {})
            namespace = resource.get('private', {}).get('payload', {}).get('runtime', {}).get('netns', '')
            if namespace:
                if '/' in namespace or namespace in ('.', '..'):
                    raise ValueError('invalid observed namespace')
                execute('execution', 'sudo test ! -e '+quote('/run/netns/'+namespace))
            for field in ('libvirt_bridge', 'root_veth'):
                if attrs.get(field):
                    query = 'sudo ip -json link show dev ' + quote(attrs[field])
                    result = lab.ssh(root, manifest, 'execution', query)
                    if result.returncode != 1 or 'does not exist' not in result.stderr:
                        raise RuntimeError('network absence not proven: '+field)
        # Preserve and independently check both reset generations using the
        # provider's read-only residue proof (including FC external sockets).
        for generation, snapshot in [('before', original)] + [(str(i), snapshot) for i, snapshot in enumerate(report['reset_states'])]:
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
