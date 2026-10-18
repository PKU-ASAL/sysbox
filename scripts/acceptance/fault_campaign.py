#!/usr/bin/env python3
"""Run live task fault/cleanup rounds only in explicitly scoped disposable labs."""
import argparse
import json
from pathlib import Path
import re
import shlex
import time
import uuid

import two_vms as lab
import host_fencing
import live_failover
import database_outage


def wait_live(execute, topology, timeout=120):
    if not re.fullmatch(r'p9-[a-zA-Z0-9-]+',topology):
        raise ValueError('test topology required')
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        query=("sudo find /var/lib/sysbox-supervisor/journal -maxdepth 1 -name '*.json' "
               "-exec jq -c 'select(.ref.topology==\""+topology+"\")' {} \\;")
        rows=execute('execution',query).splitlines()
        if len(rows)==1:
            record=json.loads(rows[0])
            if record.get('result',{}).get('Status')=='running' and not record.get('cancel_requested') and not record.get('stop_receipt'):
                checkpoint=("sudo find /var/lib/sysbox-supervisor/journal/runs -maxdepth 1 -name '*.json' "
                            "-exec jq -c '.outcomes[] | select(.run_checkpoint.request.topology==\""+topology+"\")' {} \\;")
                if len(execute('execution',checkpoint).splitlines())==1:
                    return record
        time.sleep(1)
    raise RuntimeError('live child with resource checkpoint not observed')


def verify_absence(execute, owner):
    if not owner or not re.fullmatch(r'[A-Za-z0-9_-]+',owner):
        raise ValueError('exact canonical owner required')
    for query in ('sudo docker ps -aq --filter label=sysbox.owner_uid=',
                  'sudo docker network ls -q --filter label=sysbox.owner_uid='):
        if execute('execution',query+owner).strip():
            raise RuntimeError('owned provider residue remains')


def verify_round(summary, fault, cleanup):
    if (summary.get('phase') != 'complete' or not summary.get('lab_id')
            or fault.get('lab_id') != summary['lab_id']
            or fault.get('topology') != summary.get('topology')
            or cleanup.get('topology') != summary.get('topology')
            or summary.get('metadata_deleted') is not True
            or summary.get('provider_residue') != [] or 'metadata_delete' not in cleanup):
        raise ValueError('incomplete fault/cleanup evidence')
    builds=summary.get('binary_sha256', {})
    if summary.get('binary_identity_kind')!='service-proc-exe-v1' or set(builds)!= {'control','execution'} or any(not re.fullmatch(r'[a-f0-9]{64}',v) for v in builds.values()) or len(set(builds.values()))!=1 or summary.get('binary_sha256_after')!=builds:
        raise ValueError('deployed binary identity missing or changed')
    before, after = fault.get('before', {}), fault.get('after', {})
    if (not all(before.get(k) for k in ('handle','run_id','provider_node_id')) or any(before.get(k) != after.get(k)
            for k in ('handle','run_id','provider_node_id')) or not after.get('reported')
            or not after.get('stop_receipt', {}).get('signature')):
        raise ValueError('original task completion evidence missing')
    result = after.get('result', {})
    if result.get('RemoteTermination') != 'confirmed':
        raise ValueError('task termination is unproven')
    if summary['fault'] == 'failover':
        if (not all(fault.get(k) is True for k in ('primary_stopped','primary_restored','primary_still_stopped_at_completion'))
                or fault.get('parent_on_backup', {}).get('status') != 'done'
                or not fault.get('parent_on_backup', {}).get('host_completion')
                or result.get('ExitCodeKnown') is not True or result.get('ExitCode') != 0
                or cleanup.get('phase') != 'metadata_deleted'
                or cleanup.get('state_after_destroy', {}).get('resources') != []
                or cleanup.get('state_after_destroy', {}).get('topology_uid') != summary.get('owner_uid')):
            raise ValueError('failover or cleanup not proved')
    else:
        restored = 'database_restored' if summary['fault'] == 'database' else 'network_restored'
        if (fault.get(restored) is not True or not database_outage.cancelled(fault.get('guest_during', {}))
                or not 0 <= fault.get('guest_stopped_after_seconds', -1) <= database_outage.STOP_BUDGET_SECONDS
                or cleanup.get('destroy', {}).get('status') != 'done'
                or cleanup.get('destroy', {}).get('cleanup', {}).get('empty') is not True
                or cleanup.get('state_before_destroy', {}).get('topology_uid') != summary.get('owner_uid')):
            raise ValueError('outage stop or cleanup not proved')
        database_outage.verify_stop_report(before, after)


def require_clean_execution(execute):
    if execute('execution','sudo docker ps -aq --filter label=sysbox.owner_uid').strip():
        raise RuntimeError('prior managed container remains; retain evidence and reconcile before another round')
    if execute('execution','sudo docker network ls -q --filter label=sysbox.owner_uid').strip():
        raise RuntimeError('prior managed network remains')
    query = "sudo find /var/lib/sysbox-supervisor/journal -name '*.json' -exec jq -c 'select(.result.Status==\"running\" or (.outcomes // {} | to_entries | any(.value.state==\"intent\")))' {} \\;"
    if execute('execution',query).strip():
        raise RuntimeError('prior unresolved host journal remains')


def running_builds(execute):
    result = {}
    for role, services in (('control',('sysbox-api1','sysbox-api2')),
                           ('execution',('sysbox-agent','sysbox-supervisor'))):
        hashes=[]
        for service in services:
            pid=execute(role,'sudo systemctl show '+service+' --property=MainPID --value').strip()
            if not pid.isdigit() or int(pid)<2: raise RuntimeError('service is not running: '+service)
            hashes.append(execute(role,'sudo sha256sum /proc/'+pid+'/exe').split()[0])
        if len(set(hashes))!=1: raise RuntimeError('running component build mismatch')
        result[role]=hashes[0]
    if len(set(result.values()))!=1: raise RuntimeError('running host builds differ')
    return result


def failure_diagnostics(execute):
    result={}
    for role,units in (('control','-u sysbox-api1 -u sysbox-api2'),
                       ('execution','-u sysbox-supervisor -u sysbox-agent')):
        try:
            result[role]=execute(role,"sudo journalctl "+units+" --since '10 minutes ago' --no-pager -n 80")[-32768:]
        except Exception as error:
            result[role+'_error']=str(error)[:2048]
    return result


def run(root, fault):
    root,manifest=lab.load(root)
    host_fencing.check_scope(root,manifest)
    if fault not in ('failover','database','private-network'):
        raise ValueError('unsupported fault')
    directory=root/('fault-'+fault+'-'+uuid.uuid4().hex[:12])
    directory.mkdir(mode=0o700)
    def execute(role,command):
        result=lab.ssh(root,manifest,role,command,timeout=45)
        if result.returncode:
            raise RuntimeError(role+': '+result.stderr[-1500:]+result.stdout[-1500:])
        return result.stdout
    builds=running_builds(execute)
    if len(set(builds.values()))!=1:raise RuntimeError('deployed API/Agent binaries differ')
    require_clean_execution(execute)
    # Both APIs must be healthy before starting a fresh workload.
    execute('control','sudo systemctl is-active --quiet sysbox-api1 sysbox-api2 postgresql@16-main')
    execute('execution','sudo systemctl is-active --quiet sysbox-agent sysbox-supervisor')
    for name in ('cross_host_run.py','recover_failed_cleanup.py'):
        lab.upload(root,manifest,'control',Path(__file__).parent/name,'/home/p9/'+name)
    remote='/var/tmp/'+directory.name
    seconds=90 if fault=='failover' else 600
    command=('sudo sh -c '+shlex.quote('nohup python3 /home/p9/cross_host_run.py --background --provision-seconds '+str(seconds)+
                ' --evidence '+remote+'.json > '+remote+'.log 2>&1 </dev/null & echo $!'))
    pid=execute('control',command).strip()
    if not pid.isdigit():raise RuntimeError('workload process identity unavailable')
    summary={'lab_id':manifest['id'],'fault':fault,'workload_pid':int(pid),'phase':'submitted','binary_sha256':builds,'binary_identity_kind':'service-proc-exe-v1'}
    lab.save(directory/'summary.json',summary)
    deadline=time.monotonic()+60
    while time.monotonic()<deadline:
        raw=execute('control','sudo test ! -e '+remote+'.json || sudo cat '+remote+'.json').strip()
        if raw:break
        time.sleep(1)
    else:raise RuntimeError('workload evidence missing')
    workload=json.loads(raw);topology=workload['topology'];summary['topology']=topology
    wait_live(execute,topology)
    summary['phase']='live';lab.save(directory/'summary.json',summary)
    def save(value):lab.save(directory/'fault.json',{'lab_id':manifest['id'],**value})
    try:
        if fault=='failover':live_failover.inject(execute,topology,save)
        else:database_outage.inject(topology,execute,save,fault)
        summary['phase']='fault_verified';lab.save(directory/'summary.json',summary)
        if fault=='failover':
            deadline=time.monotonic()+180
            while time.monotonic()<deadline:
                workload=json.loads(execute('control','sudo cat '+remote+'.json'))
                if workload.get('phase')=='lifecycle_finished_provider_verification_required':break
                time.sleep(1)
            else:raise RuntimeError('successful workload cleanup did not finish')
            owner=workload['state_after_destroy']['topology_uid']
            verify_absence(execute,owner)
            execute('control','sudo python3 /home/p9/cross_host_run.py --finalize --evidence '+remote+'.json')
            workload=json.loads(execute('control','sudo cat '+remote+'.json'))
        else:
            execute('control','sudo python3 /home/p9/recover_failed_cleanup.py --evidence '+remote+'.json --output '+remote+'-cleanup.json')
            workload=json.loads(execute('control','sudo cat '+remote+'-cleanup.json'))
            owner=workload['state_before_destroy']['topology_uid']
            verify_absence(execute,owner)
            execute('control','sudo python3 /home/p9/recover_failed_cleanup.py --finalize --evidence '+remote+'.json --output '+remote+'-cleanup.json')
            workload=json.loads(execute('control','sudo cat '+remote+'-cleanup.json'))
        lab.save(directory/'cleanup.json',workload)
        summary['binary_sha256_after']=running_builds(execute)
        summary.update(phase='complete',owner_uid=owner,provider_residue=[],metadata_deleted=True)
        verify_round(summary,json.loads((directory/'fault.json').read_text()),workload)
    except BaseException as error:
        summary.update(phase='failed',error=str(error))
        lab.save(directory/'diagnostics.json',failure_diagnostics(execute))
        raise
    finally:
        lab.save(directory/'summary.json',summary)
    print(json.dumps({'evidence':str(directory),'phase':summary['phase']}),flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',required=True)
    parser.add_argument('--fault',required=True,choices=['failover','database','private-network'])
    args=parser.parse_args()
    run(args.root,args.fault)
