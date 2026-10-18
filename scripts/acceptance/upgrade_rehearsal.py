#!/usr/bin/env python3
"""Archive a quiescent disposable deployment without changing its original data.

This is the evidence-preservation phase of an upgrade rehearsal, not permission
to migrate incompatible authority or replace unresolved records.
"""
import argparse
import json
from pathlib import Path
import re
import uuid
import host_fencing
import two_vms as lab


def archive_commands(destination):
    if not re.fullmatch(r'/var/tmp/sysbox-upgrade-[a-zA-Z0-9_-]+',destination):
        raise ValueError('fresh disposable archive path required')
    return [
        'sudo test ! -e '+destination+' && sudo install -d -m 0700 -o postgres -g postgres '+destination,
        'sudo systemctl stop sysbox-api1 sysbox-api2',
        'sudo -u postgres pg_dump --format=custom --file='+destination+'/database.dump sysboxp9_final_1018',
        'sudo tar -C / -cf '+destination+'/control.tar etc/sysbox-api var/lib/sysbox-api1 var/lib/sysbox-api2',
        'sudo sha256sum '+destination+'/database.dump '+destination+'/control.tar',
    ]


def restore_services(execute, evidence, save):
    errors = {}
    for role, services in (('control','sysbox-api1 sysbox-api2'),
                           ('execution','sysbox-supervisor sysbox-agent')):
        try:
            execute(role,'sudo systemctl start '+services+' && sudo systemctl is-active --quiet '+services)
        except BaseException as error:
            errors[role]=str(error)
    evidence['services_restored']=not errors
    if errors: evidence['restoration_errors']=errors
    save(evidence)
    if errors: raise RuntimeError('service restoration failed: '+', '.join(errors))


def run(root):
    root,manifest=lab.load(root)
    host_fencing.check_scope(root,manifest)
    deployment=json.loads((root/'final-1018'/'services.json').read_text())
    if deployment.get('lab_id')!=manifest['id'] or deployment.get('database')!='sysboxp9_final_1018':
        raise ValueError('unexpected deployment identity')
    directory=root/('upgrade-'+uuid.uuid4().hex[:12]);directory.mkdir(mode=0o700)
    target='/var/tmp/sysbox-'+directory.name
    evidence={'lab_id':manifest['id'],'database':deployment['database'],'phase':'preflight','archive':target}
    def execute(role,command):
        result=lab.ssh(root,manifest,role,command,timeout=120)
        if result.returncode:raise RuntimeError(role+': '+result.stderr[-1500:])
        return result.stdout.strip()
    # Refuse any test-owned container or unfinished host operation; do not infer
    # quiescence from a user request or stop daemons around live effects.
    if execute('execution','sudo docker ps -aq --filter label=sysbox.owner_uid'):
        raise RuntimeError('managed container remains; finish normal cleanup first')
    pending=execute('execution',"sudo find /var/lib/sysbox-supervisor/journal -name '*.json' -exec jq -c 'select(.result.Status==\"running\" or (.outcomes // {} | to_entries | any(.value.state==\"intent\")))' {} \\;")
    if pending:raise RuntimeError('unresolved host journal prevents upgrade rehearsal')
    lab.save(directory/'evidence.json',evidence)
    try:
        execute('execution','sudo systemctl stop sysbox-agent sysbox-supervisor')
        for command in archive_commands(target):
            output=execute('control',command)
            if 'sha256sum' in command:evidence['control_archives']=output
        execute('execution','sudo test ! -e '+target+' && sudo install -d -m 0700 '+target)
        execute('execution','sudo tar -C / -cf '+target+'/execution.tar etc/sysbox var/lib/sysbox-agent var/lib/sysbox-supervisor')
        evidence['execution_archive']=execute('execution','sudo sha256sum '+target+'/execution.tar')
        evidence['phase']='archived'
    finally:
        restore_services(execute,evidence,lambda value:lab.save(directory/'evidence.json',value))
    print(json.dumps({'evidence':str(directory),'phase':evidence['phase']}),flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',required=True)
    run(parser.parse_args().root)
