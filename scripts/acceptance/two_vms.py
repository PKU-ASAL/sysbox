#!/usr/bin/env python3
"""Disposable two-kernel P9 lab. Never mounts host directories in either guest."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import stat
import subprocess
import sys
import time
import uuid

BASE_SHA256 = "5fa5b05e5ec239858c4531485d6023b0896448c2df7c63b34f8dae6ea6051a44"
ROLES = {"control": (2, 3072, "10.77.0.10"), "execution": (4, 6144, "10.77.0.20")}


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def save(path, value):
    temp = path.with_suffix(path.suffix + ".tmp")
    with temp.open("w") as f:
        json.dump(value, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    temp.replace(path)


def free_ports(count):
    sockets = [socket.socket() for _ in range(count)]
    try:
        for sock in sockets:
            sock.bind(("127.0.0.1", 0))
        return [sock.getsockname()[1] for sock in sockets]
    finally:
        for sock in sockets:
            sock.close()


def qmp(path, command):
    with socket.socket(socket.AF_UNIX) as sock:
        sock.settimeout(5)
        sock.connect(str(path))
        stream = sock.makefile("rwb")
        greeting = json.loads(stream.readline())
        if "QMP" not in greeting:
            raise RuntimeError("invalid QMP greeting")
        for execute in ("qmp_capabilities", command):
            stream.write(json.dumps({"execute": execute}).encode() + b"\n")
            stream.flush()
            while True:
                response = json.loads(stream.readline())
                if "error" in response:
                    raise RuntimeError(str(response["error"]))
                if "return" in response:
                    break
        return response["return"]


def load(root):
    root = Path(root).resolve(strict=True)
    if root.stat().st_uid != os.geteuid() or root.stat().st_mode & 0o077:
        raise RuntimeError("lab directory must be private and owned by current user")
    manifest = json.loads((root / "lab.json").read_text())
    if manifest.get("format") != 1 or manifest.get("root") != str(root):
        raise RuntimeError("invalid lab manifest")
    return root, manifest


def ssh(root, manifest, role, command, timeout=30, retry_ready=False):
    args = [
        "ssh", "-i", str(root / "client"), "-p", str(manifest["ports"][role]),
        "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=5",
        "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + str(root / "known_hosts"),
        "p9@127.0.0.1", command]
    last = None
    attempts = 5 if retry_ready else 1
    for attempt in range(attempts):
        try:
            result = subprocess.run(args, text=True, capture_output=True, timeout=timeout)
        except subprocess.TimeoutExpired as err:
            last = err
            time.sleep(1)
            continue
        if result.returncode == 0:
            return result
        transient = "banner exchange" in result.stderr or "Connection timed out" in result.stderr
        if not retry_ready or not transient or attempt + 1 == attempts:
            return result
        time.sleep(1)
    raise RuntimeError(f"SSH {role} did not become ready: {last}")


def file_sha256(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b''):
            digest.update(chunk)
    return digest.hexdigest()


def up(root, base):
    base = Path(base).resolve(strict=True)
    digest = file_sha256(base)
    if digest != BASE_SHA256:
        raise RuntimeError("cloud image checksum mismatch")
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        raise RuntimeError("read/write access to /dev/kvm required")
    root = Path(root).resolve()
    root.mkdir(mode=0o700, parents=False, exist_ok=False)
    if len(str(root)) > 65 or "," in str(root) or "," in str(base):
        raise RuntimeError("short paths without commas required by QEMU")
    if shutil.disk_usage(root).free < 10 * 1024**3:
        raise RuntimeError("at least 10 GiB free disk required")
    lab_id = "sysbox-p9-" + uuid.uuid4().hex[:12]
    ports = free_ports(3)
    manifest = {"format": 1, "root": str(root), "id": lab_id, "base_sha256": digest,
                "ports": {"control": ports[0], "execution": ports[1], "lan": ports[2]},
                "resources": {role: {"cpus": values[0], "memory_mib": values[1]} for role, values in ROLES.items()},
                "created_at": time.time(), "commit": run("git", "rev-parse", "HEAD", capture_output=True).stdout.strip()}
    save(root / "lab.json", manifest)
    run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(root / "client"))
    public = (root / "client.pub").read_text().strip()
    known = []
    for index, (role, (cpus, memory, ip)) in enumerate(ROLES.items(), 1):
        directory = root / role
        directory.mkdir(mode=0o700)
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(directory / "host"))
        host_public = (directory / "host.pub").read_text().strip()
        known.append(f"[127.0.0.1]:{manifest['ports'][role]} {host_public}\n")
        (root / "known_hosts").write_text("".join(known))
        config = {"hostname": f"{lab_id}-{role}", "ssh_pwauth": False,
                  "users": [{"name": "p9", "groups": "sudo", "shell": "/bin/bash", "lock_passwd": True,
                             "sudo": "ALL=(ALL) NOPASSWD:ALL", "ssh_authorized_keys": [public]}],
                  "ssh_keys": {"ed25519_private": (directory / "host").read_text(), "ed25519_public": host_public}}
        (directory / "user-data").write_text("#cloud-config\n" + json.dumps(config))
        (directory / "meta-data").write_text(json.dumps({"instance-id": f"{lab_id}-{role}"}))
        nat_mac, lan_mac = f"52:54:00:77:{index:02x}:01", f"52:54:00:77:{index:02x}:02"
        (directory / "network-config").write_text(json.dumps({"version": 2, "ethernets": {
            "nat0": {"match": {"macaddress": nat_mac}, "set-name": "nat0", "dhcp4": True},
            "lan0": {"match": {"macaddress": lan_mac}, "set-name": "lan0", "addresses": [ip + "/24"]}}}))
        run("genisoimage", "-quiet", "-output", str(directory / "seed.iso"), "-volid", "cidata", "-joliet", "-rock",
            str(directory / "user-data"), str(directory / "meta-data"), str(directory / "network-config"))
        run("qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", str(base), str(directory / "disk.qcow2"), "16G")
        lan = "listen" if role == "control" else "connect"
        run("qemu-system-x86_64", "-name", f"{lab_id}-{role}", "-machine", "q35,accel=kvm", "-cpu", "host",
            "-smp", str(cpus), "-m", str(memory), "-display", "none", "-daemonize",
            "-pidfile", str(directory / "pid"), "-qmp", f"unix:{directory}/qmp,server=on,wait=off",
            "-serial", f"file:{directory}/serial.log", "-monitor", "none",
            "-drive", f"file={directory}/disk.qcow2,format=qcow2,if=virtio,discard=unmap",
            "-drive", f"file={directory}/seed.iso,format=raw,media=cdrom,readonly=on",
            "-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{manifest['ports'][role]}-:22",
            "-device", f"virtio-net-pci,netdev=nat,mac={nat_mac}",
            "-netdev", f"socket,id=lan,{lan}=127.0.0.1:{ports[2]}",
            "-device", f"virtio-net-pci,netdev=lan,mac={lan_mac}")
        print(f"started {role}, SSH port {manifest['ports'][role]}", flush=True)
    print(f"Lab manifest: {root}/lab.json; run check --root {root}", flush=True)


def check(root):
    root, manifest = load(root)
    evidence = {"lab_id": manifest["id"], "host_boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "guests": {}}
    for role in ROLES:
        if qmp(root / role / "qmp", "query-name")["name"] != manifest["id"] + "-" + role:
            raise RuntimeError("QMP VM identity mismatch")
        deadline = time.monotonic() + 300
        while True:
            result = ssh(root, manifest, role, "cat /proc/sys/kernel/random/boot_id; hostname; uname -r")
            if result.returncode == 0:
                break
            if time.monotonic() > deadline:
                raise RuntimeError(f"{role} SSH unavailable: {result.stderr}; inspect serial.log")
            print(f"waiting for {role} SSH", flush=True)
            time.sleep(3)
        boot, hostname, kernel = result.stdout.strip().splitlines()
        other = "10.77.0.20" if role == "control" else "10.77.0.10"
        probe = ssh(root, manifest, role, f"ping -c 2 -W 2 {other}")
        if probe.returncode != 0:
            raise RuntimeError(f"{role} private network failed: {probe.stdout} {probe.stderr}")
        evidence["guests"][role] = {"boot_id": boot, "hostname": hostname, "kernel": kernel}
    # The same guest path must retain a different value on each VM.
    for role in ROLES:
        result = ssh(root, manifest, role, f"printf '%s' '{role}' > /var/tmp/sysbox-p9-isolation")
        if result.returncode:
            raise RuntimeError("cannot write guest isolation probe: " + result.stderr)
    for role in ROLES:
        result = ssh(root, manifest, role, "cat /var/tmp/sysbox-p9-isolation")
        if result.returncode or result.stdout != role:
            raise RuntimeError("VM filesystems are not isolated")
    boots = [evidence["host_boot_id"]] + [v["boot_id"] for v in evidence["guests"].values()]
    if len(set(boots)) != 3:
        raise RuntimeError("host and VM boot identities must be distinct")
    nested = ssh(root, manifest, "execution", "sudo modprobe kvm_intel; sudo test -c /dev/kvm")
    if nested.returncode:
        raise RuntimeError("nested KVM missing: " + nested.stderr)
    nested = ssh(root, manifest, "execution",
                 "sudo timeout 3 qemu-system-x86_64 -machine q35,accel=kvm -cpu host "
                 "-m 128 -nodefaults -display none -monitor none -serial none -S; test $? = 124")
    if nested.returncode:
        raise RuntimeError("nested QEMU failed (install qemu-system-x86 first): " + nested.stderr)
    evidence["nested_kvm_qemu_started"] = True
    evidence["guest_filesystems_independent"] = True
    save(root / "isolation.json", evidence)
    print(json.dumps(evidence, indent=2))


def start(root):
    """Restart an offline lab without recreating disks, keys or evidence."""
    root, manifest = load(root)
    with (root / "start.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        _start_retained(root, manifest)


def _start_retained(root, manifest):
    if len(str(root)) > 65 or "," in str(root):
        raise RuntimeError("short paths without commas required by QEMU")
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        raise RuntimeError("read/write access to /dev/kvm required")
    ports = manifest["ports"]
    if (set(ports) != {"control", "execution", "lan"} or
            any(type(port) is not int or not 1024 <= port <= 65535 for port in ports.values()) or
            len(set(ports.values())) != 3):
        raise RuntimeError("invalid retained lab ports")
    resources = manifest.get('resources')
    if not isinstance(resources, dict) or set(resources) != set(ROLES):
        raise RuntimeError('invalid retained VM resources')
    for value in resources.values():
        if (not isinstance(value, dict) or set(value) != {'cpus', 'memory_mib'} or
                type(value['cpus']) is not int or not 1 <= value['cpus'] <= 64 or
                type(value['memory_mib']) is not int or not 128 <= value['memory_mib'] <= 65536):
            raise RuntimeError('invalid retained VM resources')
    # Validate both guests before starting either. An unreadable/still-existing
    # PID is uncertainty, not permission to launch another writer of its disk.
    for role in ROLES:
        directory = root / role
        if directory.is_symlink() or not directory.is_dir():
            raise RuntimeError("invalid retained VM directory")
        for name in ("disk.qcow2", "seed.iso"):
            path = directory / name
            if path.is_symlink() or not path.is_file():
                raise RuntimeError(f"invalid retained {role} {name}")
        pidfile = directory / "pid"
        if pidfile.is_symlink():
            raise RuntimeError("invalid retained VM PID file")
        if pidfile.exists():
            if not pidfile.is_file():
                raise RuntimeError("invalid retained VM PID file")
            pid = int(pidfile.read_text().strip())
            if pid <= 0:
                raise RuntimeError("invalid retained VM PID")
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                pass
            else:
                raise RuntimeError(f"{role} old PID {pid} still exists; inspect before restart")
        monitor = directory / "qmp"
        if monitor.is_symlink():
            raise RuntimeError("invalid retained monitor socket")
        if monitor.exists():
            if not stat.S_ISSOCK(monitor.lstat().st_mode):
                raise RuntimeError("invalid retained monitor socket")
            try:
                qmp(monitor, "query-name")
            except (ConnectionRefusedError, FileNotFoundError):
                pass
            else:
                raise RuntimeError(f"{role} monitor is still live")
    attempt = "restart-" + uuid.uuid4().hex[:12]
    for index, role in enumerate(ROLES, 1):
        cpus, memory = resources[role]["cpus"], resources[role]["memory_mib"]
        directory = root / role
        # Preserve old process files even after they have been proven stale.
        for name in ("pid", "qmp"):
            path = directory / name
            if path.exists():
                path.rename(directory / f"{attempt}.{name}")
        lan = "listen" if role == "control" else "connect"
        run("qemu-system-x86_64", "-name", f"{manifest['id']}-{role}",
            "-machine", "q35,accel=kvm", "-cpu", "host", "-smp", str(cpus), "-m", str(memory),
            "-display", "none", "-daemonize", "-pidfile", str(directory / "pid"),
            "-qmp", f"unix:{directory}/qmp,server=on,wait=off",
            "-serial", f"file:{directory}/{attempt}.serial.log", "-monitor", "none",
            "-drive", f"file={directory}/disk.qcow2,format=qcow2,if=virtio,discard=unmap",
            "-drive", f"file={directory}/seed.iso,format=raw,media=cdrom,readonly=on",
            "-netdev", f"user,id=nat,hostfwd=tcp:127.0.0.1:{ports[role]}-:22",
            "-device", f"virtio-net-pci,netdev=nat,mac=52:54:00:77:{index:02x}:01",
            "-netdev", f"socket,id=lan,{lan}=127.0.0.1:{ports['lan']}",
            "-device", f"virtio-net-pci,netdev=lan,mac=52:54:00:77:{index:02x}:02")
        print(f"started retained {role}; log {attempt}.serial.log", flush=True)


def stop(root):
    root, manifest = load(root)
    errors = []
    for role in reversed(ROLES):
        path = root / role / "qmp"
        if not path.exists():
            continue
        try:
            if qmp(path, "query-name")["name"] != manifest["id"] + "-" + role:
                raise RuntimeError("refusing to stop foreign VM")
            qmp(path, "quit")
            print(f"stopped {role}; retained disk and evidence")
        except (OSError, RuntimeError) as err:
            errors.append(f"{role}: {err}")
    if errors:
        raise RuntimeError("; ".join(errors))


def upload(root, manifest, role, source, destination):
    # Use SCP's SFTP transport with the same pinned guest host key as SSH.
    run("scp", "-q", "-i", str(root / "client"), "-P", str(manifest["ports"][role]),
        "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
        "-o", "UserKnownHostsFile=" + str(root / "known_hosts"),
        str(Path(source).resolve(strict=True)), "p9@127.0.0.1:" + destination)


def prepare(root):
    root, manifest = load(root)
    packages = {
        "control": "postgresql-16 curl jq",
        "execution": "docker.io qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients genisoimage iproute2 iputils-ping curl jq",
    }
    for role, names in packages.items():
        print(f"installing {role} packages; guest log /tmp/p9-packages.log", flush=True)
        result = ssh(root, manifest, role, "sudo cloud-init status --wait && sudo apt-get update -qq && "
                     "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq " + names +
                     " > /tmp/p9-packages.log 2>&1", timeout=1800, retry_ready=True)
        if result.returncode:
            raise RuntimeError(f"{role} package installation failed: {result.stderr}; inspect guest log")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["up", "start", "prepare", "check", "stop", "exec", "upload"])
    parser.add_argument("--root", required=True)
    parser.add_argument("--base")
    parser.add_argument("--role", choices=ROLES, default="control")
    parser.add_argument("--command")
    parser.add_argument("--source")
    parser.add_argument("--destination")
    args = parser.parse_args()
    if args.operation == "up":
        if not args.base:
            parser.error("up requires --base")
        up(args.root, args.base)
    elif args.operation == "start":
        start(args.root)
    elif args.operation == "check":
        check(args.root)
    elif args.operation == "prepare":
        prepare(args.root)
    elif args.operation == "stop":
        stop(args.root)
    elif args.operation == "upload":
        if not args.source or not args.destination:
            parser.error("upload requires --source and --destination")
        root, manifest = load(args.root)
        upload(root, manifest, args.role, args.source, args.destination)
    else:
        if not args.command:
            parser.error("exec requires --command")
        root, manifest = load(args.root)
        result = ssh(root, manifest, args.role, args.command, timeout=1800)
        print(result.stdout, end="")
        print(result.stderr, end="", file=sys.stderr)
        raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
