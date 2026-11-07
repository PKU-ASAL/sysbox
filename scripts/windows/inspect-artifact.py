#!/usr/bin/env python3
"""Inspect a stopped, sealed Windows image without booting or modifying it.

Adapted from #79's offline sealed-image check. This does not build images from
Apply, follow backing chains, provision guest tasks, or certify runtime readiness.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

SEALED = 'IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE'


def run_readonly(argv):
    result = subprocess.run(argv, stdin=subprocess.DEVNULL, capture_output=True,
                            timeout=120, check=False)
    if result.returncode:
        raise RuntimeError(f'{argv[0]} inspection failed (exit {result.returncode})')
    if len(result.stdout) > 1024 * 1024:
        raise ValueError('inspection response exceeds 1 MiB')
    return result.stdout


def identity(info):
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns,
            info.st_ctime_ns, info.st_mode)


def inspect_artifact(image, output, partition):
    image, output = Path(image).absolute(), Path(output).absolute()
    if output.exists() or output.is_symlink():
        raise FileExistsError('refusing to overwrite artifact report')
    if not re.fullmatch(r'/dev/sd[a-z][1-9][0-9]*', partition):
        raise ValueError('explicit Windows system partition required, e.g. /dev/sda1')
    before = image.lstat()
    if not stat.S_ISREG(before.st_mode):
        raise ValueError('image must be a regular file, not a symlink')
    if before.st_mode & 0o222:
        raise ValueError('image must be read-only; stop the builder and seal the artifact first')
    disk = json.loads(run_readonly(['qemu-img', 'info', '--output=json', '-f', 'qcow2', str(image)]))
    if disk.get('format') != 'qcow2' or disk.get('backing-filename') or disk.get('full-backing-filename'):
        raise ValueError('expected a standalone qcow2 artifact without a backing chain')
    if disk.get('encrypted') or disk.get('dirty-flag'):
        raise ValueError('encrypted or dirty image cannot be certified offline')
    raw = run_readonly(['guestfish', '--ro', '--format=qcow2', '-a', str(image),
                       '-m', partition, 'cat', '/Windows/Setup/State/State.ini'])
    encoding = 'utf-16' if raw.startswith((b'\xff\xfe', b'\xfe\xff')) else 'utf-8-sig'
    text = raw.decode(encoding)
    matches = re.findall(r'^\s*ImageState\s*=\s*"?([^"\r\n]+)', text, re.MULTILINE)
    if len(matches) != 1 or matches[0].strip() != SEALED:
        raise ValueError('Windows image is not generalized and sealed for OOBE')
    digest = hashlib.sha256()
    with image.open('rb') as stream:
        if identity(os.fstat(stream.fileno())) != identity(before):
            raise ValueError('image changed during inspection')
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    if identity(image.lstat()) != identity(before):
        raise ValueError('image changed during inspection')
    report = {'format': 1, 'kind': 'qcow2', 'guest_family': 'windows',
              'source': str(image), 'size': before.st_size,
              'sha256': digest.hexdigest(), 'image_state': SEALED,
              'system_partition': partition, 'runtime_accepted': False,
              'inspected_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    # Link publishes a fully written report atomically without replacing another
    # writer's output. Temporary and final paths share the same filesystem.
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=output.parent, delete=False) as stream:
            temporary = Path(stream.name)
            json.dump(report, stream, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, output)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('image', type=Path)
    parser.add_argument('output', type=Path)
    parser.add_argument('--system-partition', required=True)
    args = parser.parse_args()
    try:
        inspect_artifact(args.image, args.output, args.system_partition)
    except (ValueError, OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        parser.exit(1, f'artifact inspection: {error}\n')
    print(f'Offline image inspection recorded in {args.output}; runtime acceptance remains required.')


if __name__ == '__main__':
    main()
