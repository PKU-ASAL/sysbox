#!/usr/bin/env python3
"""Archive an enrolled deployment; archiving alone does not prove rebuild success."""
import argparse
import json
import re
import shlex
import uuid
import host_fencing
import two_vms as lab


def archive_commands(destination, database='sysboxp9_final_1018'):
    if not re.fullmatch(r'/var/tmp/sysbox-upgrade-[a-zA-Z0-9_-]+', destination) or not re.fullmatch(r'[a-z][a-z0-9_]{0,39}', database):
        raise ValueError('fresh disposable archive path and database required')
    return [
        'sudo test ! -e '+destination+' && sudo install -d -m 0700 -o postgres -g postgres '+destination,
        'sudo systemctl stop sysbox-api1 sysbox-api2',
        'sudo -u postgres pg_dump --format=custom --file='+destination+'/database.dump '+database,
        'sudo tar -C / -cf '+destination+'/control.tar etc/sysbox-api var/lib/sysbox-api1 var/lib/sysbox-api2',
        'sudo sha256sum '+destination+'/database.dump '+destination+'/control.tar',
    ]


def pending_journal_command():
    code = '''import pathlib,json
root=pathlib.Path('/var/lib/sysbox-host/journal')
if not root.is_dir(): raise RuntimeError('journal directory missing')
for path in root.rglob('*.json'):
    record=json.loads(path.read_text())
    if record.get('result',{}).get('Status')=='running' or any(value.get('state')=='intent' for value in record.get('outcomes',{}).values()):
        print(path)
'''
    return 'sudo python3 -c '+shlex.quote(code)


def require_offline(states):
    if set(states) != {'control', 'execution'}:
        raise ValueError('both host service states required')
    for role, state in states.items():
        if state.splitlines() != ['inactive', 'inactive']:
            raise ValueError(role+' writers must already be inactive')


def restore_services(execute, evidence, save):
    errors = {}
    for role, services in (('control','sysbox-api1 sysbox-api2'),
                           ('execution','sysbox-host sysbox-agent')):
        try:
            execute(role,'sudo systemctl start '+services+' && sudo systemctl is-active --quiet '+services)
        except BaseException as error:
            errors[role]=str(error)
    evidence['services_restored']=not errors
    if errors: evidence['restoration_errors']=errors
    save(evidence)
    if errors: raise RuntimeError('service restoration failed: '+', '.join(errors))


def scan_journals(execute, evidence, archive_only):
    try:
        return execute('execution',pending_journal_command())
    except Exception as error:
        if not archive_only:
            raise
        evidence['journal_scan_error']=str(error)
        evidence['journal_state']='unknown; raw files preserved without interpretation'
        return ''


def run(root, deployment_name='final-1018', archive_only=False):
    root,manifest=lab.load(root)
    host_fencing.check_scope(root,manifest)
    if not re.fullmatch(r'[a-z][a-z0-9_-]{0,39}', deployment_name):
        raise ValueError('invalid deployment name')
    deployment=json.loads((root/deployment_name/'services.json').read_text())
    if deployment.get('lab_id')!=manifest['id']:
        raise ValueError('unexpected deployment identity')
    # Validate identifiers before any remote command or archive creation.
    archive_commands('/var/tmp/sysbox-upgrade-validation', deployment['database'])
    directory=root/('upgrade-'+uuid.uuid4().hex[:12]);directory.mkdir(mode=0o700)
    target='/var/tmp/sysbox-'+directory.name
    evidence={'lab_id':manifest['id'],'database':deployment['database'],'phase':'preflight',
              'archive':target,'archive_only':archive_only,'deployment':deployment_name}
    def save():
        lab.save(directory/'evidence.json',evidence)
    def execute(role,command,allowed=(0,)):
        result=lab.ssh(root,manifest,role,command,timeout=120)
        if result.returncode not in allowed:raise RuntimeError(role+': '+result.stderr[-1500:])
        return result.stdout.strip()
    save()
    restore=False
    try:
        if archive_only:
            states={role:execute(role,'sudo systemctl is-active '+services,(0,3)) for role,services in
                    (('control','sysbox-api1 sysbox-api2'),('execution','sysbox-host sysbox-agent'))}
            require_offline(states)
            evidence['writer_states']=states
        elif execute('execution','sudo docker ps -aq --filter label=sysbox.owner_uid'):
            raise RuntimeError('managed container remains; finish normal cleanup first')
        pending=scan_journals(execute,evidence,archive_only)
        evidence['unresolved_journals']=pending.splitlines()
        save()
        if pending and not archive_only:
            raise RuntimeError('unresolved host journal prevents service upgrade; offline archive-only is available')
        if not archive_only:
            restore=True
            execute('execution','sudo systemctl stop sysbox-agent sysbox-host')
        for command in archive_commands(target,deployment['database']):
            if archive_only and 'systemctl stop' in command: continue
            output=execute('control',command)
            if 'sha256sum' in command:evidence['control_archives']=output
        execute('execution','sudo test ! -e '+target+' && sudo install -d -m 0700 '+target)
        execute('execution','sudo tar -C / -cf '+target+'/execution.tar etc/sysbox-host etc/sysbox-worker var/lib/sysbox-agent var/lib/sysbox-host')
        evidence['execution_archive']=execute('execution','sudo sha256sum '+target+'/execution.tar')
        evidence['phase']='archived'
    except BaseException as error:
        evidence['error']=str(error)
        raise
    finally:
        save()
        if restore:
            restore_services(execute,evidence,lambda value:lab.save(directory/'evidence.json',value))
    print(json.dumps({'evidence':str(directory),'phase':evidence['phase']}),flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',required=True)
    parser.add_argument('--deployment',default='final-1018')
    parser.add_argument('--archive-only',action='store_true')
    args=parser.parse_args()
    run(args.root,args.deployment,args.archive_only)
