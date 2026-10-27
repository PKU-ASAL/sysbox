#!/usr/bin/env python3
"""Verify enrolled sshd deployment over the isolated two-VM private network.

This tests service installation/reload/recovery and administrator login. It does
not substitute for API/Agent managed-key or runtime resource acceptance.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import uuid

import two_vms as lab


def acceptance(root, helper):
    root, manifest = lab.load(root)
    helper = Path(helper).resolve(strict=True)
    name = "sysbox-p9-ssh-" + uuid.uuid4().hex[:12]
    evidence_dir = root / name
    evidence_dir.mkdir(mode=0o700)
    remote_root = "/var/lib/" + name
    unit = name + ".service"

    def remote(role, command):
        result = lab.ssh(root, manifest, role, command, timeout=45)
        if result.returncode:
            raise RuntimeError(f"{role}: {result.stdout}\n{result.stderr}")
        return result.stdout.strip()

    def stage(name, content, destination, mode="0644"):
        source = evidence_dir / name
        source.write_text(content)
        target = "/home/p9/" + source.name
        lab.upload(root, manifest, "execution", source, target)
        remote("execution", f"sudo install -m {mode} {shlex.quote(target)} {shlex.quote(destination)} && rm -- {shlex.quote(target)}")

    boots = {role: remote(role, "cat /proc/sys/kernel/random/boot_id") for role in lab.ROLES}
    if len(set(boots.values())) != 2:
        raise RuntimeError("two independent guest kernels required")
    control_key = "/home/p9/" + name
    remote("control", f"ssh-keygen -q -t ed25519 -N '' -f {shlex.quote(control_key)}")
    public = remote("control", f"cat {shlex.quote(control_key + '.pub')}")
    port = int(remote("execution", "python3 -c 'import socket; s=socket.socket(); s.bind((\"10.77.0.20\",0)); print(s.getsockname()[1])'"))
    remote("execution", f"sudo mkdir -m 0755 {shlex.quote(remote_root)}")
    staged_helper = "/home/p9/" + name + "-helper"
    lab.upload(root, manifest, "execution", helper, staged_helper)
    remote("execution", f"sudo install -m 0755 {staged_helper} {remote_root}/helper && rm -- {staged_helper}")
    remote("execution", f"sudo ssh-keygen -q -t ed25519 -N '' -f {remote_root}/host-key")
    host_public = remote("execution", f"sudo cat {remote_root}/host-key.pub")
    stage("administrator.pub", public + "\n", remote_root + "/administrator.pub")
    main = (f"ListenAddress 10.77.0.20\nPort {port}\nHostKey {remote_root}/host-key\n"
            f"PidFile {remote_root}/sshd.pid\nAuthorizedKeysFile {remote_root}/administrator.pub\n"
            "UsePAM yes\nStrictModes yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"
            f"AllowUsers p9\nInclude {remote_root}/sysbox.conf\n")
    stage("sshd_config", main, remote_root + "/sshd_config")
    profile = {"version": 1, "root": remote_root + "/journal",
               "config_path": remote_root + "/sshd_config", "drop_in_path": remote_root + "/sysbox.conf",
               "helper": "/usr/bin/true", "command_user": "root", "sshd": "/usr/sbin/sshd",
               "systemctl": "/usr/bin/systemctl", "service_unit": unit,
               "listeners": [f"10.77.0.20:{port}"],
               "probes": [{"User": "p9", "RemoteHost": "control", "RemoteAddress": "10.77.0.10",
                           "LocalAddress": "10.77.0.20", "LocalPort": port,
                           "AuthorizedKeysFile": remote_root + "/administrator.pub"}]}
    stage("profile.json", json.dumps(profile), remote_root + "/profile.json", "0600")
    service = ("[Unit]\nAfter=network.target\n[Service]\nType=simple\n"
               f"ExecStart=/usr/sbin/sshd -D -e -f {remote_root}/sshd_config\n"
               "ExecReload=/bin/kill -HUP $MAINPID\n")
    stage(unit, service, "/etc/systemd/system/" + unit)
    known = f"[10.77.0.20]:{port} {host_public}\n"
    # Only public host-key data crosses this command; private keys stay in control.
    remote("control", f"printf %s {shlex.quote(known)} > {shlex.quote(control_key + '.known')}")
    login = (f"ssh -i {control_key} -p {port} -o BatchMode=yes -o IdentitiesOnly=yes "
             f"-o StrictHostKeyChecking=yes -o UserKnownHostsFile={control_key}.known "
             "-o ConnectTimeout=5 p9@10.77.0.20 'printf p9-ssh-ready'")
    evidence = {"lab_id": manifest["id"], "name": name, "guest_boots": boots,
                "helper_sha256": hashlib.sha256(helper.read_bytes()).hexdigest(),
                "scope": "sshd deployment and preserved administrator login; not managed-key lifecycle"}
    try:
        remote("execution", f"sudo systemctl daemon-reload && sudo systemctl start {unit}")
        if remote("control", login) != "p9-ssh-ready":
            raise RuntimeError("initial administrator login failed")
        for operation in ("install", "recover"):
            remote("execution", f"sudo {remote_root}/helper --ssh-service-{operation} {remote_root}/profile.json")
            if remote("control", login) != "p9-ssh-ready":
                raise RuntimeError(f"administrator login failed after {operation}")
            evidence[operation] = "passed"
        actual = remote("execution", f"sha256sum {remote_root}/sshd_config").split()[0]
        if actual != hashlib.sha256(main.encode()).hexdigest():
            raise RuntimeError("administrator main configuration was modified")
        evidence["main_config_unchanged"] = True
        journal = remote("execution", f"sudo cat {remote_root}/journal/deployment.json")
        if json.loads(journal)["phase"] != "ready":
            raise RuntimeError("deployment did not persist readiness")
    finally:
        remote("execution", f"sudo systemctl stop {unit} && ! systemctl is-active --quiet {unit} && sudo rm -- /etc/systemd/system/{unit} && sudo systemctl daemon-reload")
        if remote("execution", f"ss -H -ltn 'sport = :{port}'"):
            raise RuntimeError("test SSH listener survived service cleanup")
        evidence["test_service_stopped"] = True
        lab.save(evidence_dir / "result.json", evidence)
    print(evidence_dir / "result.json")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True)
    parser.add_argument("--helper", required=True)
    args = parser.parse_args()
    acceptance(args.root, args.helper)
