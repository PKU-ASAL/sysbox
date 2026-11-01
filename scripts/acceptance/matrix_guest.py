#!/usr/bin/env python3
"""Run one signed guest probe through the API on the disposable control VM."""
import json
import re
import sys
import time
from pathlib import Path
from cross_host_run import request_json


def main():
    topology, node, raw = sys.argv[1:]
    if not re.fullmatch(r'p9-mixed-[a-f0-9]{10}', topology) or node not in ('docker', 'firecracker', 'libvirt'):
        raise ValueError('mixed acceptance scope required')
    argv = json.loads(raw)
    if not isinstance(argv, list) or not argv or argv[0] not in ('true', 'ping', 'touch', 'test') or not all(isinstance(a, str) for a in argv):
        raise ValueError('acceptance probe argv required')
    config = json.loads(Path('/etc/sysbox-api/api1.json').read_text())
    def request(method, path, body=None):
        return request_json('http://10.77.0.10:19870', config['api']['token'], method, path, body)
    task = request('POST', f'/v1/topologies/{topology}/nodes/{node}/executions', {'argv': argv})
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        result = request('GET', '/v1/executions/' + task['id'])
        if result['status'] == 'completed':
            if result['result']['exit_code'] != 0:
                raise RuntimeError('guest probe returned failure: ' + task['id'])
            print('guest probe passed: ' + task['id'])
            return
        if result['status'] in ('failed', 'cancelled', 'unknown'):
            raise RuntimeError('guest probe not successful: ' + task['id'])
        time.sleep(1)
    raise RuntimeError('guest probe remains unresolved: ' + task['id'])


if __name__ == '__main__':
    main()
