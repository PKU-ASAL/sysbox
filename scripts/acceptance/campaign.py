#!/usr/bin/env python3
"""Run exact acceptance gates and reconcile original P0–P9 requirements.

A passed contract gate is evidence only for its declared scope. Missing gates,
skips, changed source and explicitly remaining work never count as completion.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

STAGES = {'P0': 4, 'P1': 5, 'P2': 7, 'P3': 7, 'P4': 6,
          'P5': 5, 'P6': 5, 'P7': 6, 'P8': 4, 'P9': 6}


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def verify_events(raw, expected, expected_package=None):
    running, passed, packages = set(), set(), set()
    for line in raw.splitlines():
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except (ValueError, TypeError) as exc:
            raise ValueError('invalid Go event stream') from exc
        action, package, test = event.get('Action'), event.get('Package'), event.get('Test', '')
        if not package:
            raise ValueError('event has no package identity')
        if expected_package is not None and package != expected_package:
            raise ValueError('event package differs from selected package')
        key = (package, test)
        if action in ('skip', 'fail'):
            raise ValueError(f'{action}: {package}/{test}')
        if action == 'run':
            running.add(key)
        elif action == 'pass':
            if test:
                if key not in running:
                    raise ValueError('pass without execution')
                running.remove(key)
                passed.add(test)
            else:
                packages.add(package)
    if running or not packages or set(expected) - passed:
        raise ValueError('missing or unfinished required test')


def original_requirements(root):
    source = root / 'docs/design/2026-10-07-full-migration-distributed-execution-plan.md'
    out, stage, number = {}, None, 0
    for line in source.read_text().splitlines():
        if line.startswith('### P'):
            match = re.match(r'### (P\d+)：', line)
            stage = match[1] if match and match[1] in STAGES else None
            number = 0
        elif line.startswith('## '):
            stage = None
        elif stage and line.startswith('- [ ] '):
            number += 1
            out[f'{stage}.{number}'] = line[6:]
        elif stage and line.startswith('验收：'):
            out[f'{stage}.acceptance'] = line[3:]
        elif stage == 'P9' and line.startswith('总验收'):
            out['P9.acceptance'] = line
    expected = {f'{stage}.{i}' for stage, count in STAGES.items() for i in range(1, count + 1)}
    expected.update(f'{stage}.acceptance' for stage in STAGES)
    if set(out) != expected:
        raise ValueError('original requirement inventory changed; update catalog explicitly')
    return out


def validate_catalog(catalog, root):
    original = original_requirements(root)
    requirements, gates = catalog.get('requirements', []), catalog.get('gates', [])
    if catalog.get('version') != 1 or len(requirements) != len(original) or {r['id'] for r in requirements} != set(original):
        raise ValueError('catalog must cover every original requirement exactly once')
    ids = [gate['id'] for gate in gates]
    if len(ids) != len(set(ids)):
        raise ValueError('duplicate gate')
    for gate in gates:
        if not re.fullmatch(r'[a-z0-9-]+', gate['id']) or not re.fullmatch(r'\./[A-Za-z0-9_/]+', gate['package']):
            raise ValueError('invalid gate identity/package')
        directory = (root / gate['package']).resolve()
        if not directory.is_relative_to(root.resolve()) or not directory.is_dir():
            raise ValueError('invalid package directory')
        tests = gate['tests']
        if not tests or len(set(tests)) != len(tests):
            raise ValueError('exact unique tests required')
        source = '\n'.join(path.read_text() for path in directory.glob('*_test.go'))
        for test in tests:
            if not re.fullmatch(r'Test[A-Za-z0-9_]+', test) or not re.search(r'func\s+' + re.escape(test) + r'\s*\(', source):
                raise ValueError('missing test: ' + test)
        if gate['kind'] not in ('contract', 'real-provider', 'upgrade'):
            raise ValueError('invalid evidence kind')
        if set(gate.get('artifact_env', [])) - set(gate.get('requires_env', [])):
            raise ValueError('artifact inputs must be declared as required environment')
        for name in gate.get('requires_env', []):
            if not re.fullmatch(r'[A-Z][A-Z0-9_]+', name):
                raise ValueError('invalid environment key')
    for requirement in requirements:
        if requirement['text'] != original[requirement['id']]:
            raise ValueError('requirement wording differs from original plan')
        if set(requirement['gates']) - set(ids) or not isinstance(requirement['remaining'], list):
            raise ValueError('invalid requirement gate mapping')


def load_catalog(path, root):
    catalog = json.loads(path.read_text())
    validate_catalog(catalog, root)
    return catalog


def source_identity(root):
    # Include untracked implementation and gate files; ignore docs and generated
    # build artifacts. A doc-only result update cannot invalidate code evidence.
    paths = subprocess.check_output(['git', 'ls-files', '-co', '--exclude-standard', '-z'], cwd=root).split(b'\0')
    digest = hashlib.sha256()
    for name in sorted(set(paths)):
        if not name:
            continue
        path = root / os.fsdecode(name)
        if path.suffix not in ('.go', '.py', '.sh', '.json', '.yaml', '.yml', '.hcl', '.bin') and path.name not in ('Makefile', 'go.mod', 'go.sum'):
            continue
        if path.is_symlink():
            raise ValueError('source symlink unsupported: ' + str(path))
        digest.update(name + b'\0')
        digest.update(path.read_bytes() if path.exists() else b'<deleted>')
        digest.update(b'\0')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    return commit, digest.hexdigest()


def requirement_status(requirement, statuses):
    if requirement['remaining']:
        return 'open'
    if not requirement['gates'] or any(statuses.get(gate) != 'passed' for gate in requirement['gates']):
        return 'unverified'
    return 'verified'


def fingerprint_artifact(path):
    path = path.resolve(strict=True)
    if not path.is_file():
        raise ValueError('artifact must be a regular file')
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b''):
            digest.update(chunk)
    return {'path': str(path), 'sha256': digest.hexdigest(), 'size': path.stat().st_size}


def verify_artifacts(artifacts):
    for recorded in artifacts:
        actual=fingerprint_artifact(Path(recorded['path']))
        if any(actual[key] != recorded[key] for key in ('path','sha256','size')):
            raise ValueError('tested external artifact changed')


def validate_evidence(directory, result, gate, source_hash):
    if result['source_sha256'] != source_hash or result['exit_code'] != 0 or result['status'] != 'passed':
        raise ValueError('failed or stale gate evidence')
    if result['id'] != gate['id'] or result['package'] != gate['package'] or result['tests'] != gate['tests']:
        raise ValueError('gate selector differs from evidence')
    relative = Path(result['events'])
    if relative.is_absolute() or len(relative.parts) != 1 or relative.suffix != '.jsonl':
        raise ValueError('unsafe evidence path')
    path = directory / relative
    if path.is_symlink() or not path.resolve().is_relative_to(directory.resolve()):
        raise ValueError('unsafe evidence path')
    raw = path.read_bytes()
    if sha256(raw) != result['events_sha256']:
        raise ValueError('event stream digest mismatch')
    expected_package = 'github.com/oslab/sysbox/' + gate['package'][2:]
    if result.get('event_package') != expected_package:
        raise ValueError('missing or changed event package binding')
    if 'artifacts' not in result:
        raise ValueError('artifact inventory missing')
    if {item.get('env') for item in result['artifacts']} != set(gate.get('artifact_env', [])):
        raise ValueError('required artifact bindings missing')
    verify_artifacts(result['artifacts'])
    verify_events(raw.decode(), gate['tests'], expected_package)


def reconcile(root, catalog, evidence_dirs):
    commit, source_hash = source_identity(root)
    statuses, evidence = {}, []
    gates = {gate['id']: gate for gate in catalog['gates']}
    for directory in evidence_dirs:
        report = json.loads((directory / 'run.json').read_text())
        for result in report['gates']:
            gate = gates.get(result['id'])
            if gate is None:
                raise ValueError('unknown evidence gate')
            try:
                validate_evidence(directory, result, gate, source_hash)
            except (ValueError, KeyError, OSError) as exc:
                if statuses.get(gate['id']) != 'passed':
                    statuses[gate['id']] = 'unverified'
                evidence.append({'gate': gate['id'], 'status': 'unverified', 'reason': str(exc)})
            else:
                statuses[gate['id']] = 'passed'
                evidence.append({'gate': gate['id'], 'status': 'passed', 'directory': str(directory)})
    rows = [dict(requirement, status=requirement_status(requirement, statuses)) for requirement in catalog['requirements']]
    return {'version': 1, 'commit': commit, 'source_sha256': source_hash, 'requirements': rows,
            'evidence': evidence, 'complete': all(row['status'] == 'verified' for row in rows)}


def run_gates(root, catalog, selected, output, go):
    # New directory only: no stale success artifacts or overwritten evidence.
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    commit, source_hash = source_identity(root)
    report = {'version': 1, 'commit': commit, 'source_sha256': source_hash,
              'started_at': datetime.now(timezone.utc).isoformat(), 'gates': []}
    for gate in catalog['gates']:
        if gate['id'] not in selected:
            continue
        missing = [name for name in gate.get('requires_env', []) if not os.environ.get(name)]
        result = {'id': gate['id'], 'kind': gate['kind'], 'package': gate['package'], 'tests': gate['tests'],
                  'source_sha256': source_hash, 'status': 'blocked', 'exit_code': None,
                  'event_package': 'github.com/oslab/sysbox/' + gate['package'][2:], 'artifacts': []}
        if missing:
            result['reason'] = 'missing environment: ' + ', '.join(missing)
        else:
            for name in gate.get('artifact_env', []):
                if not os.environ.get(name):
                    raise ValueError('required artifact missing: ' + name)
                artifact = fingerprint_artifact(Path(os.environ[name]))
                artifact['env'] = name
                result['artifacts'].append(artifact)
            args = [go, 'test', '-json', '-count=1', '-timeout=' + gate.get('timeout', '3m'),
                    '-run=^(' + '|'.join(re.escape(test) for test in gate['tests']) + ')$']
            if gate.get('tags'):
                args += ['-tags=' + gate['tags']]
            if gate.get('race'):
                args += ['-race']
            args += [gate['package']]
            path = output / (gate['id'] + '.jsonl')
            print('Running ' + gate['id'], flush=True)
            with path.open('wb') as stream, (output / (gate['id'] + '.stderr')).open('wb') as stderr:
                process = subprocess.run(args, cwd=root, stdout=stream, stderr=stderr, check=False)
            raw = path.read_bytes()
            result.update(exit_code=process.returncode, events=path.name, events_sha256=sha256(raw), status='passed')
            try:
                validate_evidence(output, result, gate, source_identity(root)[1])
            except (ValueError, KeyError) as exc:
                result.update(status='failed', reason=str(exc))
        report['gates'].append(result)
        (output / 'run.json').write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n')
        print(gate['id'] + ': ' + result['status'], flush=True)
    return all(result['status'] == 'passed' for result in report['gates'])


def main():
    root = Path(__file__).resolve().parents[2]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['check', 'run', 'reconcile'])
    parser.add_argument('--gate', action='append', default=[])
    parser.add_argument('--output', type=Path)
    parser.add_argument('--evidence', action='append', type=Path, default=[])
    parser.add_argument('--go', default='go')
    args = parser.parse_args()
    catalog = load_catalog(root / 'scripts/acceptance/campaign.json', root)
    if args.operation == 'check':
        print(f"catalog valid: {len(catalog['requirements'])} requirements, {len(catalog['gates'])} gates")
        return 0
    if args.operation == 'run':
        known = {gate['id'] for gate in catalog['gates']}
        if not args.gate or set(args.gate) - known or not args.output:
            parser.error('explicit known --gate and new --output directory required')
        return 0 if run_gates(root, catalog, set(args.gate), args.output, args.go) else 1
    report = reconcile(root, catalog, args.evidence)
    encoded = json.dumps(report, indent=2, ensure_ascii=False) + '\n'
    if args.output:
        with args.output.open('x') as stream:
            stream.write(encoded)
    else:
        print(encoded, end='')
    return 0 if report['complete'] else 2


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
