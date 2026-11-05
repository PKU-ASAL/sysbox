#!/usr/bin/env python3
"""Fresh host installation from a checksum-verified Sysbox release bundle.

Enrollment/API setup remains explicit. This command never starts services or
replaces an existing identity, configuration, binary or service unit.
"""
import argparse
import grp
import hashlib
import json
import os
import platform
import pwd
import re
import subprocess
from pathlib import Path
from urllib.parse import urlsplit

REQUIRED = (
    'sysbox', 'sysbox-init', 'sysbox-netns', 'build-metadata.json',
    'deploy/systemd/sysbox-host.service', 'deploy/systemd/sysbox-agent.service',
    'deploy/docker/agent.yaml',
)


def machine_arch():
    value = platform.machine().lower()
    return {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(value, value)


def regular(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError(f'expected regular file: {path}')
    return path.read_bytes()


def verify_bundle(bundle):
    entries = {}
    for line in regular(bundle / 'INSTALL-SHA256SUMS').decode().splitlines():
        digest, name = line.split('  ', 1)
        relative = Path(name)
        if (not re.fullmatch(r'[0-9a-f]{64}', digest) or relative.is_absolute()
                or '..' in relative.parts or name in entries):
            raise ValueError('invalid checksum manifest')
        for part in (bundle / relative).parents:
            if part == bundle:
                break
            if part.is_symlink():
                raise ValueError('symlink in checksum path')
        if hashlib.sha256(regular(bundle / name)).hexdigest() != digest:
            raise ValueError(f'checksum mismatch: {name}')
        entries[name] = digest
    if not set(REQUIRED).issubset(entries):
        raise ValueError('checksum manifest omits required files')
    meta = json.loads(regular(bundle / 'build-metadata.json'))
    if meta.get('os') != 'linux' or meta.get('architecture') != machine_arch():
        raise ValueError('bundle architecture mismatch')
    if not re.fullmatch(r'[0-9a-f]{40}', meta.get('commit', '')) or not meta.get('version'):
        raise ValueError('bundle has no immutable build identity')
    return meta


def worker_account(create=False):
    """Refuse conflicting IDs instead of changing an existing user's identity."""
    expected = ((grp.getgrnam, grp.getgrgid, 'gr_gid'),
                (pwd.getpwnam, pwd.getpwuid, 'pw_uid'))
    missing = []
    for by_name, by_id, field in expected:
        try:
            record = by_name('sysbox-agent')
            if getattr(record, field) != 32000:
                raise ValueError('sysbox-agent must use UID/GID 32000')
            if field == 'pw_uid' and record.pw_gid != 32000:
                raise ValueError('sysbox-agent primary GID must be 32000')
            missing.append(False)
        except KeyError:
            try:
                by_id(32000)
            except KeyError:
                missing.append(True)
            else:
                raise ValueError('UID/GID 32000 is already allocated')
    if create:
        if missing[0]:
            subprocess.run(['groupadd', '--gid', '32000', 'sysbox-agent'], check=True)
        if missing[1]:
            subprocess.run(['useradd', '--uid', '32000', '--gid', '32000',
                            '--no-create-home', '--shell', '/usr/sbin/nologin',
                            'sysbox-agent'], check=True)


def installation_files(bundle, inputs):
    meta = verify_bundle(bundle)
    identity = json.loads(regular(inputs / 'identity.json'))
    if identity.get('token') or not all(isinstance(identity.get(k), str) and identity[k]
                                      for k in ('id', 'api_url', 'secret')):
        raise ValueError('identity must contain worker credentials, not an administrator token')
    url = urlsplit(identity['api_url'])
    if url.scheme not in ('http', 'https') or not url.hostname or url.username or url.password:
        raise ValueError('identity API URL is invalid')
    key = regular(inputs / 'host.key')
    host_public = regular(inputs / 'host.pub')
    issuer_public = regular(inputs / 'issuer.pub')
    if len(key) != 64 or len(host_public) != 32 or len(issuer_public) != 32 or key[32:] != host_public:
        raise ValueError('expected matching raw Ed25519 host key and 32-byte public keys')
    # Reuse the checksummed release executable's runtime validator. stdin keeps
    # key material out of process arguments; never echo subprocess diagnostics.
    try:
        result = subprocess.run([str(bundle / 'sysbox'), 'host-key-check'],
                                input=key + host_public, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=10, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ValueError('host signing key validator unavailable') from error
    if result.returncode != 0:
        raise ValueError('invalid host signing key; release validator rejected input')
    host_config = regular(inputs / 'sysbox.yaml')
    if not host_config.strip() or b'REPLACE_' in host_config:
        raise ValueError('host config is empty or still contains REPLACE_ placeholders')
    files = {}
    def add(destination, data, mode=0o644, worker=False):
        files[destination] = (data, mode, worker)
    for binary in ('sysbox', 'sysbox-init', 'sysbox-netns'):
        add('usr/local/bin/' + binary, regular(bundle / binary), 0o755)
    for service in ('sysbox-host.service', 'sysbox-agent.service'):
        add('etc/systemd/system/' + service, regular(bundle / 'deploy/systemd' / service))
    add('etc/sysbox-host/sysbox.yaml', host_config, 0o600)
    add('etc/sysbox-host/host.key', key, 0o600)
    add('etc/sysbox-host/identity.json', regular(inputs / 'identity.json'), 0o600)
    add('etc/sysbox-worker/agent.yaml', regular(bundle / 'deploy/docker/agent.yaml'), 0o444)
    add('etc/sysbox-worker/host.pub', host_public, 0o444)
    add('etc/sysbox-worker/issuer.pub', issuer_public, 0o444)
    add('var/lib/sysbox-agent/identity.json', regular(inputs / 'identity.json'), 0o600, True)
    add('usr/local/share/sysbox/build-metadata.json', json.dumps(meta, sort_keys=True).encode() + b'\n')
    return files


def check_destinations(root, files):
    for relative in files:
        destination = root / relative
        if destination.exists() or destination.is_symlink():
            raise ValueError(f'refusing existing destination: {destination}')
        for parent in destination.parents:
            if parent.is_symlink() or (parent.exists() and not parent.is_dir()):
                raise ValueError(f'unsafe destination parent: {parent}')
            if parent == root:
                break
    for relative in ('etc/sysbox-host', 'etc/sysbox-worker', 'var/lib/sysbox-agent'):
        target = root / relative
        if target.exists():
            raise ValueError(f'refusing existing configuration/identity directory: {target}')


def install(bundle, inputs, root, check=False):
    bundle, inputs, root = (Path(p).absolute() for p in (bundle, inputs, root))
    files = installation_files(bundle, inputs)
    check_destinations(root, files)
    live = root == Path('/')
    if live:
        if os.geteuid() != 0:
            raise ValueError('host installation requires root; use --check with a staging --root for inspection')
        worker_account()
    if check:
        return
    if live:
        worker_account(create=True)
    # All inputs and conflicts are checked before any files are installed.
    # A partial I/O failure leaves installed files in place for explicit review;
    # reruns never overwrite those files or silently rotate credentials.
    for relative, (data, mode, worker) in files.items():
        destination = root / relative
        missing = []
        parent = destination.parent
        while not parent.exists():
            missing.append(parent)
            parent = parent.parent
        for directory in reversed(missing):
            directory.mkdir(mode=0o755)
            directory.chmod(0o755)  # Explicit even under root umask 077.
        if relative.startswith('etc/sysbox-host/'):
            destination.parent.chmod(0o700)
        elif relative.startswith('var/lib/sysbox-agent/'):
            destination.parent.chmod(0o700)
            if live:
                os.chown(destination.parent, 32000, 32000)
        fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            os.fchmod(stream.fileno(), mode)
            if live:
                os.fchown(stream.fileno(), 32000 if worker else 0, 32000 if worker else 0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=Path, default=Path(__file__).resolve().parent)
    parser.add_argument('--inputs', type=Path, required=True,
                        help='directory containing identity.json, host.key, host.pub, issuer.pub, sysbox.yaml')
    parser.add_argument('--root', type=Path, default=Path('/'), help='alternate staging root; does not create system users')
    parser.add_argument('--check', action='store_true', help='validate without changing files/users/services')
    args = parser.parse_args()
    try:
        install(args.bundle, args.inputs, args.root, args.check)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'install: {error}\n')
    print('Checks passed.' if args.check else 'Installed. Services were not started. Run systemctl daemon-reload, then enable --now sysbox-host and sysbox-agent.')


if __name__ == '__main__':
    main()
