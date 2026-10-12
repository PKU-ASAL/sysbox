#!/usr/bin/env python3
"""Isolated two-VM public API/Agent SSH key lifecycle and real-login gate.

Uses an explicitly pre-enrolled Docker image. Does not claim automatic service
enrollment or migration of historical authority. Failed runs retain evidence.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import time
import uuid

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

import two_vms as lab


def service_configs(name, port, token, password):
    if not re.fullmatch(r"[a-z][a-z0-9]{1,30}", name):
        raise ValueError("isolated deployment identifier required")
    root = "/var/lib/sysbox-p9/" + name
    dsn = f"postgres://{name}:{password}@10.77.0.10:5432/{name}?sslmode=disable&topology={{topology}}"
    configs = {}
    for role in ("api", "host", "agent"):
        home = root + "/" + role
        configs[role] = {"version": 1, "paths": {"home": home, "runs_dir": home + "/runs", "workspaces_dir": home + "/workspaces", "cache": home + "/cache"}}
    configs["api"].update({"state": {"backend": dsn}, "api": {"listen": f"10.77.0.10:{port}", "token": token, "execution_grant": {"signing_key_file": root + "/issuer.key", "host_public_key_files": {name: root + "/host.pub"}}}})
    configs["host"].update({"state": {"backend": dsn}, "agent": {"execution_grant_public_key_file": root + "/issuer.pub"}, "host_supervisor": {"socket": root + "/socket/host.sock", "root": root + "/journal", "signing_key_file": root + "/host.key", "worker_group_id": 2000}})
    configs["agent"]["agent"] = {"host_supervisor_socket": root + "/socket/host.sock", "execution_session_file": root + "/agent/session.json", "execution_grant_public_key_file": root + "/issuer.pub"}
    return configs


def authentication_rejected(code, stderr):
    return code == 255 and "Permission denied" in stderr


def container_absent(code, stdout, stderr, identity):
    return code == 1 and stdout.strip() == "[]" and "no such object: " + identity in stderr.lower()


def acceptance(root, binary, helper):
    os.umask(0o077)
    root, manifest = lab.load(root)
    binary, helper = Path(binary).resolve(strict=True), Path(helper).resolve(strict=True)
    name = "sshgate" + uuid.uuid4().hex[:12]
    evidence_dir = root / name
    evidence_dir.mkdir(mode=0o700)
    remote_root = "/var/lib/sysbox-p9/" + name
    evidence = {"name": name, "lab_id": manifest["id"], "phase": "setup", "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "helper_sha256": hashlib.sha256(helper.read_bytes()).hexdigest(), "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "scope": "pre-enrolled Docker guest; public API/Agent managed SSH lifecycle and independent real logins"}

    def save():
        lab.save(evidence_dir / "result.json", evidence)

    def remote(role, command, timeout=45):
        result = lab.ssh(root, manifest, role, command, timeout=timeout)
        if result.returncode:
            raise RuntimeError(f"{role}: {result.stdout}\n{result.stderr}")
        return result.stdout.strip()

    def upload(role, source, destination, mode="0600", owner="root"):
        staged = "/home/p9/" + name + "-" + source.name
        lab.upload(root, manifest, role, source, staged)
        remote(role, f"sudo install -m {mode} -o {owner} -g {owner} {shlex.quote(staged)} {shlex.quote(destination)} && rm -- {shlex.quote(staged)}")

    def stage(role, filename, content, destination, mode="0600", owner="root"):
        source = evidence_dir / filename
        source.write_text(content)
        upload(role, source, destination, mode, owner)

    def port(role, address):
        return int(remote(role, f"python3 -c 'import socket; s=socket.socket(); s.bind(({json.dumps(address)},0)); print(s.getsockname()[1])'"))

    save()
    boots = {role: remote(role, "cat /proc/sys/kernel/random/boot_id") for role in lab.ROLES}
    if len(set(boots.values())) != 2:
        raise RuntimeError("independent kernels required")
    evidence["guest_boots"] = boots
    api_port, ssh_port = port("control", "10.77.0.10"), port("execution", "10.77.0.20")
    token, password = secrets.token_hex(32), secrets.token_hex(32)
    configs = service_configs(name, api_port, token, password)
    for role in lab.ROLES:
        remote(role, f"sudo install -d -m 0755 /var/lib/sysbox-p9 && sudo mkdir -m 0755 {remote_root}")
        upload(role, binary, remote_root + "/sysbox", "0755")
    for keyname in ("issuer", "host"):
        key = Ed25519PrivateKey.generate()
        seed = key.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption())
        public = key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
        (evidence_dir / (keyname + ".key")).write_bytes(seed + public)
        (evidence_dir / (keyname + ".pub")).write_bytes(public)
    for role, keyname, owner, mode in (("control", "issuer.key", "sysboxapi", "0600"), ("control", "host.pub", "sysboxapi", "0600"), ("execution", "issuer.pub", "root", "0644"), ("execution", "host.key", "root", "0600")):
        upload(role, evidence_dir / keyname, remote_root + "/" + keyname, mode, owner)
    listeners = remote("control", "sudo -u postgres psql -Atc 'SHOW listen_addresses'")
    if not ({"10.77.0.10", "*"} & {item.strip() for item in listeners.split(",")}):
        raise RuntimeError("existing PostgreSQL must already listen on the private network; cluster configuration is not modified")
    sql = f"CREATE ROLE {name} LOGIN PASSWORD '{password}';\nCREATE DATABASE {name} OWNER {name};\n"
    stage("control", "database.sql", sql, remote_root + "/database.sql", owner="postgres")
    remote("control", f"sudo -u postgres psql -v ON_ERROR_STOP=1 -f {remote_root}/database.sql")
    # A separate included HBA rule affects this new role/database only.
    stage("control", "database.conf", f"host {name} {name} 10.77.0.0/24 scram-sha-256\n", remote_root + "/database.conf", "0644")
    remote("control", f"sudo sh -c {shlex.quote('printf "\\ninclude '+remote_root+'/database.conf\\n" >> /etc/postgresql/16/main/pg_hba.conf')} && sudo -u postgres psql -Atc 'SELECT pg_reload_conf()'")
    for role, config_name, user in (("control", "api", "sysboxapi"), ("execution", "host", "root"), ("execution", "agent", "sysboxagent")):
        remote(role, f"sudo install -d -m 0700 -o {user} -g {user} {remote_root}/{config_name}")
        stage(role, config_name + ".json", json.dumps(configs[config_name]), remote_root + "/" + config_name + ".json", owner=user)

    def start_unit(role, suffix, user, command):
        unit = name + "-" + suffix + ".service"
        text = f"[Unit]\nAfter=network-online.target\n[Service]\nType=simple\nUser={user}\nEnvironment=SYSBOX_CONFIG={remote_root}/{suffix}.json\nExecStart={remote_root}/sysbox {command}\nRestart=on-failure\nRestartSec=1\n"
        stage(role, unit, text, "/etc/systemd/system/" + unit, "0644")
        remote(role, f"sudo systemctl daemon-reload && sudo systemctl start {unit} && sudo systemctl is-active {unit}")
        return unit

    api_unit = start_unit("control", "api", "sysboxapi", f"serve --config {remote_root}/api.json")
    url = f"http://10.77.0.10:{api_port}"

    def api(method, path, payload=None):
        encoded = base64.b64encode(json.dumps({"method": method, "path": path, "body": payload}).encode()).decode()
        code = ("import json,urllib.request,base64; "
                f"cfg=json.load(open({remote_root + '/api.json'!r})); p=json.loads(base64.b64decode({encoded!r})); "
                "data=None if p['body'] is None else json.dumps(p['body']).encode(); "
                f"r=urllib.request.Request({url!r}+p['path'],data=data,method=p['method'],headers={{'Authorization':'Bearer '+cfg['api']['token'],'Content-Type':'application/json','X-Sysbox-Roles':'admin'}}); "
                "print(urllib.request.urlopen(r,timeout=20).read().decode())")
        output = remote("control", "sudo python3 -c " + shlex.quote(code))
        return json.loads(output) if output else None

    deadline = time.monotonic() + 30
    while True:
        try:
            api("GET", "/v1/agents")
            break
        except RuntimeError:
            if time.monotonic() > deadline:
                raise
            time.sleep(1)
    registration = f"#!/bin/sh\nset -eu\n{remote_root}/sysbox agent register --api {url} --id {name} --name {name} --substrates docker --identity {remote_root}/agent/identity.json --token {shlex.quote(token)}\n"
    stage("execution", "register.sh", registration, remote_root + "/register.sh")
    remote("execution", f"sudo sh {remote_root}/register.sh >/dev/null && sudo install -m 0600 {remote_root}/agent/identity.json {remote_root}/host-identity.json && sudo chown sysboxagent:sysboxagent {remote_root}/agent/identity.json")
    host_unit = start_unit("execution", "host", "root", f"host-supervisor --config {remote_root}/host.json --identity {remote_root}/host-identity.json")
    agent_unit = start_unit("execution", "agent", "sysboxagent", f"agent start --config {remote_root}/agent.json --identity {remote_root}/agent/identity.json")
    evidence.update({"api_url": url, "api_unit": api_unit, "host_unit": host_unit, "agent_unit": agent_unit, "remote_root": remote_root, "ssh_port": ssh_port})
    save()
    print("Isolated services ready: " + name, flush=True)
    for key in ("administrator", "first", "rotated"):
        remote("control", f"sudo ssh-keygen -q -t ed25519 -N '' -f {remote_root}/{key}")
    public = {key: remote("control", f"sudo cat {remote_root}/{key}.pub") for key in ("administrator", "first", "rotated")}
    build = remote_root + "/image"
    remote("execution", f"sudo mkdir -m 0700 {build}")
    upload("execution", helper, build + "/sysbox-guest-task", "0755")
    upload("execution", evidence_dir / "issuer.pub", build + "/issuer.pub")
    stage("execution", "administrator.pub", public["administrator"] + "\n", build + "/administrator.pub")
    sshd = "Port 22\nListenAddress 0.0.0.0\nHostKey /etc/ssh/ssh_host_ed25519_key\nPidFile /run/sshd.pid\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers sysboxguest\nAuthorizedKeysFile /etc/sysbox/administrator.pub\nAuthorizedKeysCommand /usr/local/bin/sysbox-guest-task --ssh-authorized-keys %u\nAuthorizedKeysCommandUser root\n"
    stage("execution", "sshd_config", sshd, build + "/sshd_config")
    dockerfile = "FROM alpine:3.22\nRUN apk add --no-cache openssh-server && adduser -D sysboxguest && passwd -d sysboxguest && mkdir -p /run/sshd /etc/sysbox\nCOPY sysbox-guest-task /usr/local/bin/sysbox-guest-task\nCOPY issuer.pub /etc/sysbox/grant-public-key\nCOPY administrator.pub /etc/sysbox/administrator.pub\nCOPY sshd_config /etc/ssh/sshd_config\nRUN chmod 0755 /usr/local/bin/sysbox-guest-task && chmod 0600 /etc/sysbox/grant-public-key && chmod 0644 /etc/sysbox/administrator.pub\nENTRYPOINT [\"/bin/sh\",\"-c\",\"ssh-keygen -A && exec /usr/sbin/sshd -D -e\"]\n"
    stage("execution", "Dockerfile", dockerfile, build + "/Dockerfile")
    remote("execution", f"sudo docker build -t {name}:local {build}", timeout=180)
    subnet = 1 + int(name[-2:], 16) % 250
    hcl = f'''resource "sysbox_network" "ssh" {{
  cidr = "172.29.{subnet}.0/24"
  nat = true
}}
resource "sysbox_image" "base" {{
  substrate = "docker"
  source = "{name}:local"
  kind = "oci"
  architecture = "amd64"
  guest_family = "linux"
}}
resource "sysbox_node" "web" {{
  substrate = "docker"
  image = sysbox_image.base.id
  link "ssh" {{
    network = sysbox_network.ssh.id
    ip = "172.29.{subnet}.10/24"
  }}
  connection {{
    type = "ssh"
    user = "sysboxguest"
  }}
  port {{
    name = "ssh"
    target = 22
    published = {ssh_port}
    protocol = "tcp"
    exposure = "host"
    host_ip = "10.77.0.20"
  }}
}}
'''
    revision = api("POST", "/v1/revisions", {"files": {"field.sysbox.hcl": hcl}})["revision"]
    evidence.update({"topology": name, "revision": revision, "phase": "apply"}); save()

    def wait(path):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            result = api("GET", path)
            if result.get("status") in ("done", "completed"):
                evidence.setdefault("completed_operations", {})[path] = result
                save()
                return result
            if result.get("status") in ("failed", "cancelled"):
                raise RuntimeError("operation failed: " + json.dumps(result))
            time.sleep(1)
        raise RuntimeError("operation timed out: " + path)

    run = api("POST", f"/v1/topologies/{name}/apply", {"revision": revision, "agent_id": name})
    evidence["apply_run"] = run; save()
    wait("/v1/runs/" + run["run_id"])
    state = api("GET", f"/v1/topologies/{name}/state")
    node = next(r for r in state["resources"] if r["address"] == "sysbox_node.web")
    evidence["node"] = node; save()
    container = node["external_id"]
    if not re.fullmatch(r"[a-f0-9]{64}", container):
        raise RuntimeError("immutable Docker ID required")
    host_key = remote("execution", f"sudo docker exec {container} cat /etc/ssh/ssh_host_ed25519_key.pub")
    stage("control", "known_hosts", f"[10.77.0.20]:{ssh_port} {host_key}\n", remote_root + "/known_hosts")

    def login(key, allowed):
        command = f"sudo ssh -i {remote_root}/{key} -p {ssh_port} -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile={remote_root}/known_hosts -o ConnectTimeout=5 sysboxguest@10.77.0.20 'printf managed-ssh-ok'"
        result = lab.ssh(root, manifest, "control", command, timeout=15)
        if allowed:
            if result.returncode or result.stdout.strip() != "managed-ssh-ok":
                raise RuntimeError(f"{key} login failed: {result.stderr}")
        elif not authentication_rejected(result.returncode, result.stderr):
            raise RuntimeError(f"{key} was not explicitly rejected by authentication: {result.returncode} {result.stderr}")
        evidence.setdefault("login_checks", []).append({"key": key, "allowed": allowed, "phase": evidence["phase"], "verified_at": time.time()})
        save()

    def mutation(action, key, revision, delete=False):
        request = {"id": name + "-" + action, "node": "web", "operation": "ssh_access_delete" if delete else "ssh_access_apply", "access_id": "operator", "expected_revision": revision}
        if key:
            request["authorized_keys"] = [public[key]]
        accepted = api("POST", f"/v1/topologies/{name}/ssh-access", request)
        evidence[action + "_operation"] = accepted; save()
        result = wait("/v1/node-operations/" + accepted["id"])
        evidence[action + "_result"] = result; save()

    login("administrator", True); login("first", False)
    mutation("create", "first", 0)
    login("first", True); login("administrator", True)
    evidence["phase"] = "created_login_verified"; save()
    mutation("rotate", "rotated", 1)
    login("first", False); login("rotated", True); login("administrator", True)
    evidence["phase"] = "rotation_verified"; save()
    remote("control", f"sudo systemctl restart {api_unit}")
    remote("execution", f"sudo systemctl restart {host_unit} {agent_unit}")
    login("rotated", True); login("administrator", True)
    # Poll the original operation through the reconstructed API before revoking.
    wait("/v1/node-operations/" + name + "-rotate")
    mutation("revoke", None, 2, True)
    login("first", False); login("rotated", False); login("administrator", True)
    evidence["phase"] = "revocation_and_restart_verified"; save()
    runtime_hcl = hcl + f'''resource "sysbox_ssh_access" "runtime" {{
  node = sysbox_node.web.id
  authorized_keys = [{json.dumps(public['first'])}]
}}
'''
    runtime_revision = api("POST", "/v1/revisions", {"files": {"field.sysbox.hcl": runtime_hcl}})["revision"]
    run = api("POST", f"/v1/topologies/{name}/apply", {"revision": runtime_revision})
    evidence["runtime_apply_run"] = run; save()
    wait("/v1/runs/" + run["run_id"])
    login("first", True); login("rotated", False)
    evidence["phase"] = "runtime_ssh_verified"; save()
    run = api("POST", f"/v1/topologies/{name}/reset", {"revision": runtime_revision, "target": "sysbox_node.web"})
    evidence["reset_run"] = run; save()
    wait("/v1/runs/" + run["run_id"])
    replacement_state = api("GET", f"/v1/topologies/{name}/state")
    replacement = next(r for r in replacement_state["resources"] if r["address"] == "sysbox_node.web")
    if replacement["resource_uid"] != node["resource_uid"] or replacement["generation"] != node["generation"] + 1 or replacement["external_id"] == container:
        raise RuntimeError("reset did not preserve UID and advance resource generation")
    old_container, container = container, replacement["external_id"]
    if not re.fullmatch(r"[a-f0-9]{64}", container):
        raise RuntimeError("immutable replacement Docker ID required")
    evidence["replacement_node"] = replacement; save()
    host_key = remote("execution", f"sudo docker exec {container} cat /etc/ssh/ssh_host_ed25519_key.pub")
    stage("control", "known_hosts", f"[10.77.0.20]:{ssh_port} {host_key}\n", remote_root + "/known_hosts")
    login("first", True); login("rotated", False); login("administrator", True)
    evidence["phase"] = "reset_ssh_login_verified"; save()
    run = api("POST", f"/v1/topologies/{name}/destroy", {})
    evidence["destroy_run"] = run; save()
    wait("/v1/runs/" + run["run_id"])
    if remote("execution", f"sudo docker ps -aq --filter label=sysbox.owner_uid={shlex.quote(node['owner_uid'])}"):
        raise RuntimeError("owned containers remain after Destroy")
    if remote("execution", f"sudo docker network ls -q --filter label=sysbox.owner_uid={shlex.quote(node['owner_uid'])}"):
        raise RuntimeError("owned networks remain after Destroy")
    for removed in (old_container, container):
        inspected = lab.ssh(root, manifest, "execution", f"sudo docker inspect {removed}")
        if not container_absent(inspected.returncode, inspected.stdout, inspected.stderr, removed):
            raise RuntimeError("exact container absence was not verified")
    evidence["provider_absence_verified"] = {"owner_uid": node["owner_uid"], "containers": [old_container, container], "owner_containers_empty": True, "owner_networks_empty": True}
    save()
    evidence["metadata_delete"] = api("DELETE", f"/v1/topologies/{name}")
    remote("execution", f"sudo systemctl stop {agent_unit} {host_unit}")
    remote("control", f"sudo systemctl stop {api_unit}")
    evidence["phase"] = "passed_services_stopped"; save()
    print(evidence_dir / "result.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--helper", required=True)
    args = parser.parse_args()
    acceptance(args.root, args.binary, args.helper)
