#!/usr/bin/env python3
"""Deploy test-only Sysbox services into a verified two_vms.py lab."""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import secrets
import shlex

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

import two_vms as lab


def remote(root, manifest, role, command):
    result = lab.ssh(root, manifest, role, command, timeout=1800)
    if result.returncode:
        # Commands reference staged secret files, not their contents.
        raise RuntimeError(f"{role}: {result.stdout}\n{result.stderr}")
    print(f"{role}: {result.stdout.strip()}", flush=True)


def unit(command, user, config):
    return ("[Unit]\nAfter=network-online.target\nWants=network-online.target\n"
            "[Service]\nType=simple\nUser=" + user + "\nEnvironment=SYSBOX_CONFIG=" + config +
            "\nExecStart=/usr/local/bin/sysbox " + command +
            "\nRestart=on-failure\nRestartSec=2\n[Install]\nWantedBy=multi-user.target\n")


def database_config(database, password):
    if not re.fullmatch(r"[a-z][a-z0-9_]{0,39}", database):
        raise ValueError("database must be a simple lowercase SQL identifier")
    dsn = f"postgres://{database}:{password}@10.77.0.10:5432/{database}?sslmode=disable&topology={{topology}}"
    sql = (f"DO $$ BEGIN IF EXISTS (SELECT FROM pg_roles WHERE rolname='{database}') OR EXISTS (SELECT FROM pg_database WHERE datname='{database}') THEN RAISE EXCEPTION 'deployment database or role already exists; preserve it and select a new deployment'; END IF; END $$;\n"
           f"CREATE ROLE {database} LOGIN PASSWORD '{password}';\n"
           f"CREATE DATABASE {database} OWNER {database};\n"
           "ALTER SYSTEM SET listen_addresses='127.0.0.1,10.77.0.10';\n")
    return dsn, sql


def release_files(binary, metadata_path):
    target, = [row for row in json.loads(Path(metadata_path).read_text())['targets'] if row['architecture']=='amd64']
    files = {}
    for name in ('sysbox','sysbox-init','sysbox-netns'):
        path=Path(binary) if name=='sysbox' else Path(binary).parent/name
        if lab.file_sha256(path) != target[name.replace('-','_')+'_sha256']:
            raise ValueError('release executable checksum mismatch: '+name)
        files[name]=path.resolve(strict=True)
    return files


def deploy(root, binary, database="sysboxp9", deployment="deployment", release_metadata=None):
    binaries=release_files(binary,release_metadata) if release_metadata else {"sysbox":binary}
    if not re.fullmatch(r"[a-z][a-z0-9_-]{0,39}", deployment):
        raise ValueError("deployment must be a simple directory name")
    database_config(database, "validation")
    root, manifest = lab.load(root)
    proof = json.loads((root / "isolation.json").read_text())
    if proof.get("lab_id") != manifest["id"] or not proof.get("nested_kvm_qemu_started"):
        raise RuntimeError("run two_vms.py prepare/check first")
    secret_dir = root / deployment
    secret_dir.mkdir(mode=0o700, exist_ok=True)
    binding = secret_dir / "binding.json"
    expected = {"lab_id": manifest["id"], "database": database, "deployment": deployment}
    if binding.exists() and json.loads(binding.read_text()) != expected:
        raise RuntimeError("deployment is bound to another database or lab")
    if not binding.exists():
        lab.save(binding, expected)
    credentials = secret_dir / "credentials.json"
    if not credentials.exists():
        lab.save(credentials, {"api_token": secrets.token_hex(32), "db_password": secrets.token_hex(32)})
    private = json.loads(credentials.read_text())
    for name in ("issuer", "host"):
        path = secret_dir / (name + ".key")
        if not path.exists():
            key = Ed25519PrivateKey.generate()
            seed = key.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption())
            public = key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
            path.write_bytes(seed + public)
            (secret_dir / (name + ".pub")).write_bytes(public)
    password = private["db_password"]
    dsn, sql = database_config(database, password)
    configs = {}
    for index in (1, 2):
        home = f"/var/lib/sysbox-api{index}"
        configs[f"api{index}"] = {
            "version": 1, "paths": {"home": home, "runs_dir": home + "/runs", "workspaces_dir": home + "/workspaces", "cache": home + "/cache"},
            "state": {"backend": dsn},
            "api": {"listen": f"10.77.0.10:{19870 + index}", "token": private["api_token"],
                    "execution_grant": {"signing_key_file": "/etc/sysbox-api/issuer.key", "host_public_key_files": {"execution-p9": "/etc/sysbox-api/host.pub"}}}}
    configs["supervisor"] = {
        "version": 1, "paths": {"home": "/var/lib/sysbox-supervisor", "runs_dir": "/var/lib/sysbox-supervisor/runs", "workspaces_dir": "/var/lib/sysbox-supervisor/workspaces", "cache": "/var/lib/sysbox-supervisor/cache"},
        "state": {"backend": dsn}, "agent": {"execution_grant_public_key_file": "/etc/sysbox/issuer.pub"},
        "host_supervisor": {"socket": "/run/sysbox-supervisor/host.sock", "root": "/var/lib/sysbox-supervisor/journal", "signing_key_file": "/etc/sysbox/host.key", "worker_group_id": 2000}}
    configs["agent"] = {
        "version": 1, "paths": {"home": "/var/lib/sysbox-agent", "runs_dir": "/var/lib/sysbox-agent/runs", "workspaces_dir": "/var/lib/sysbox-agent/workspaces", "cache": "/var/lib/sysbox-agent/cache"},
        "agent": {"host_supervisor_socket": "/run/sysbox-supervisor/host.sock", "execution_session_file": "/var/lib/sysbox-agent/session.json", "execution_grant_public_key_file": "/etc/sysbox/issuer.pub"}}

    def stage(role, name, content):
        source = secret_dir / name
        if content is not None:
            source.write_text(content)
        lab.upload(root, manifest, role, source, "/home/p9/" + name)

    stage("control", "p9-db.sql", sql)
    # Fail before replacing any service binary/configuration if the chosen
    # database is already present. Interrupted deployments require inspection.
    remote(root, manifest, "control", "sudo -u postgres psql -v ON_ERROR_STOP=1 < /home/p9/p9-db.sql")
    for role in lab.ROLES:
        for name, executable in binaries.items():
            lab.upload(root, manifest, role, executable, "/home/p9/"+name)
            remote(root, manifest, role, "sudo install -m 0755 /home/p9/"+name+" /usr/local/bin/"+name)
    remote(root, manifest, "control", "sudo apt-get install -y -qq haproxy >/tmp/p9-proxy-packages.log 2>&1; id sysboxapi >/dev/null 2>&1 || sudo useradd --system --create-home sysboxapi")
    remote(root, manifest, "control",
           f"sudo sh -c 'grep -q \"host {database} {database} 10.77.0.0/24 scram-sha-256\" /etc/postgresql/16/main/pg_hba.conf || echo \"host {database} {database} 10.77.0.0/24 scram-sha-256\" >> /etc/postgresql/16/main/pg_hba.conf' && "
           "sudo systemctl restart postgresql && sudo install -d -m 0750 -o sysboxapi -g sysboxapi /etc/sysbox-api")
    for name in ("issuer.key", "host.pub"):
        stage("control", name, None)
        remote(root, manifest, "control", f"sudo install -m 0600 -o sysboxapi -g sysboxapi /home/p9/{name} /etc/sysbox-api/{name}")
    for name in ("api1", "api2"):
        stage("control", name + ".json", json.dumps(configs[name]))
        stage("control", name + ".service", unit(f"serve --config /etc/sysbox-api/{name}.json", "sysboxapi", f"/etc/sysbox-api/{name}.json"))
        remote(root, manifest, "control", f"sudo install -d -m 0700 -o sysboxapi -g sysboxapi /var/lib/sysbox-{name} && "
               f"sudo install -m 0600 -o sysboxapi -g sysboxapi /home/p9/{name}.json /etc/sysbox-api/{name}.json && "
               f"sudo install -m 0644 /home/p9/{name}.service /etc/systemd/system/sysbox-{name}.service && "
               f"sudo systemctl daemon-reload && sudo systemctl restart sysbox-{name}")
    # The default multi-second health window exceeds the host's online
    # authorization budget. Fail over within the bounded transport retries.
    proxy = "global\n  maxconn 512\ndefaults\n  mode tcp\n  timeout connect 500ms\n  timeout client 1h\n  timeout server 1h\nfrontend p9\n  bind 10.77.0.10:19870\n  default_backend api\nbackend api\n  default-server inter 100ms fastinter 100ms downinter 100ms fall 1 rise 1\n  server api1 10.77.0.10:19871 check\n  server api2 10.77.0.10:19872 check backup\n"
    stage("control", "p9-proxy.cfg", proxy)
    remote(root, manifest, "control", "sudo install -m 0644 /home/p9/p9-proxy.cfg /etc/haproxy/haproxy.cfg && sudo systemctl restart haproxy && sudo systemctl is-active sysbox-api1 sysbox-api2 haproxy")

    remote(root, manifest, "execution", "getent group sysboxagent >/dev/null || sudo groupadd -g 2000 sysboxagent; id sysboxagent >/dev/null 2>&1 || sudo useradd --system -g sysboxagent --home-dir /var/lib/sysbox-agent sysboxagent; "
           "sudo install -d -m 0700 -o sysboxagent -g sysboxagent /var/lib/sysbox-agent; sudo install -d -m 0755 /etc/sysbox; sudo install -d -m 0700 /var/lib/sysbox-supervisor")
    for name in ("issuer.pub", "host.key"):
        stage("execution", name, None)
        mode = "0644" if name.endswith("pub") else "0600"
        remote(root, manifest, "execution", f"sudo install -m {mode} /home/p9/{name} /etc/sysbox/{name}")
    for name in ("supervisor", "agent"):
        stage("execution", name + ".json", json.dumps(configs[name]))
        mode = "0600" if name == "supervisor" else "0644"
        remote(root, manifest, "execution", f"sudo install -m {mode} /home/p9/{name}.json /etc/sysbox/{name}.json")
    registration = ("#!/bin/sh\nset -eu\nif ! sudo test -f /var/lib/sysbox-agent/identity.json; then\n"
                    "sudo /usr/local/bin/sysbox agent register --api http://10.77.0.10:19870 --id execution-p9 --name execution-p9 --substrates docker "
                    "--identity /var/lib/sysbox-agent/identity.json --token " + shlex.quote(private["api_token"]) + "\nfi\n"
                    "sudo install -m 0600 /var/lib/sysbox-agent/identity.json /etc/sysbox/host-identity.json\n"
                    "sudo chown sysboxagent:sysboxagent /var/lib/sysbox-agent/identity.json\n")
    stage("execution", "register.sh", registration)
    remote(root, manifest, "execution", "sh /home/p9/register.sh")
    for name, user, command in (
        ("supervisor", "root", "host-supervisor --config /etc/sysbox/supervisor.json --identity /etc/sysbox/host-identity.json"),
        ("agent", "sysboxagent", "agent start --config /etc/sysbox/agent.json --identity /var/lib/sysbox-agent/identity.json"),
    ):
        stage("execution", name + ".service", unit(command, user, f"/etc/sysbox/{name}.json"))
        remote(root, manifest, "execution", f"sudo install -m 0644 /home/p9/{name}.service /etc/systemd/system/sysbox-{name}.service && sudo systemctl daemon-reload && sudo systemctl restart sysbox-{name}")
    remote(root, manifest, "execution", "sudo systemctl is-active sysbox-agent sysbox-supervisor")
    binary_digest = lab.file_sha256(binary)
    services = {"executables": {name: lab.file_sha256(path) for name,path in binaries.items()}, "lab_id": manifest["id"], "binary_sha256": binary_digest, "database": database,
             "api_url": "http://10.77.0.10:19870", "agent_id": "execution-p9",
             "lifecycle_acceptance": "required"}
    lab.save(secret_dir / "services.json", services)
    print("Services started; lifecycle acceptance remains required.")


if __name__ == "__main__":
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--database", default="sysboxp9")
    parser.add_argument("--deployment", default="deployment")
    parser.add_argument("--release-metadata", type=Path)
    args = parser.parse_args()
    deploy(args.root, Path(args.binary).resolve(strict=True), args.database, args.deployment, args.release_metadata)
