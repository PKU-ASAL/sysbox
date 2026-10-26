#!/usr/bin/env python3
"""Verify post-effect ENOSPC boundaries on an enrolled disposable VM."""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import uuid

import campaign
import host_fencing as fencing
import two_vms

TESTS = ['TestENOSPCAfterEffectCannotReplayOrInventCompletionE2E',
         'TestENOSPCStopReceiptAndReportRecoveryE2E']
PACKAGE = 'github.com/oslab/sysbox/pkg/effectgate'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    root, manifest = two_vms.load(args.root)
    fencing.check_scope(root, manifest)
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    source = Path(__file__).resolve().parents[2]
    source_hash = campaign.source_identity(source)[1]
    binary = (args.output / 'effectgate.test').resolve()
    build = ['go', 'test', '-c', '-tags=e2e', '-o', str(binary), './pkg/effectgate']
    subprocess.run(build, cwd=source, env=dict(os.environ, CGO_ENABLED='0'), check=True)
    remote = '/var/tmp/sysbox-enospc-' + str(uuid.uuid4())
    report = {'version': 1, 'lab_id': manifest['id'], 'source_sha256': source_hash,
              'build': build, 'tests': TESTS, 'iterations': 2,
              'binary_sha256': two_vms.file_sha256(binary),
              'status': 'failed', 'remote_directory': remote,
              'scope': '1MiB private tmpfs; post-effect outcome, stop receipt and report acknowledgement'}
    quote = shlex.quote
    try:
        fencing.wait_boot(root, manifest, 'execution')
        fencing.guest(root, manifest, 'execution', 'mkdir -m 700 ' + quote(remote))
        two_vms.upload(root, manifest, 'execution', binary, remote + '/effectgate.test')
        actual = fencing.guest(root, manifest, 'execution',
                               'sha256sum ' + quote(remote + '/effectgate.test')).split()[0]
        if actual != report['binary_sha256']:
            raise RuntimeError('remote test binary differs')
        fencing.guest(root, manifest, 'execution',
                      'sudo chown root:root ' + quote(remote + '/effectgate.test') +
                      ' && sudo chmod 700 ' + quote(remote + '/effectgate.test'))
        command = ('sudo unshare --mount --propagation private env SYSBOX_ENOSPC_E2E=1 ' +
                   quote(remote + '/effectgate.test') + ' -test.v -test.run=' +
                   quote('^(' + '|'.join(TESTS) + ')$') + ' -test.count=2 -test.timeout=2m')
        result = two_vms.ssh(root, manifest, 'execution', command, timeout=140)
        (args.output / 'stdout.log').write_text(result.stdout)
        (args.output / 'stderr.log').write_text(result.stderr)
        events = subprocess.run(['go', 'tool', 'test2json', '-t', '-p', PACKAGE],
                                input=result.stdout, text=True, capture_output=True, check=True).stdout
        (args.output / 'events.jsonl').write_text(events)
        report['events_sha256'] = campaign.sha256(events.encode())
        if result.returncode:
            raise RuntimeError('ENOSPC test failed; retained evidence')
        campaign.verify_events(events.encode(), TESTS, PACKAGE)
        rows = [json.loads(line) for line in events.splitlines()]
        for test in TESTS:
            for action in ('run', 'pass'):
                if sum(row.get('Test') == test and row.get('Action') == action for row in rows) != 2:
                    raise RuntimeError('exactly two successful iterations required')
        if campaign.source_identity(source)[1] != source_hash:
            raise RuntimeError('source changed during acceptance')
        fencing.guest(root, manifest, 'execution', 'sudo rm -rf -- ' + quote(remote))
        report.update(status='passed', cleanup='private mount namespace exited; test binary removed')
    except subprocess.TimeoutExpired as error:
        for name, raw in (('stdout.log', error.stdout), ('stderr.log', error.stderr)):
            (args.output / name).write_bytes(raw.encode() if isinstance(raw, str) else raw or b'')
        report['error'] = 'SSH timeout; retained remote directory for process inspection'
        raise
    except Exception as error:
        report['error'] = str(error)
        raise
    finally:
        (args.output / 'run.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))


if __name__ == '__main__':
    main()
